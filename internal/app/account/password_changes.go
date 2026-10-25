package account

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"telesrv/internal/domain"
	"telesrv/internal/otpdelivery"
)

// mutatePassword keeps all factors in one serialized write boundary. Delivery
// happens after commit; a failed delivery may remove only its own pending value.
func (s *Service) mutatePassword(ctx context.Context, userID int64, apply func(*domain.PasswordSettings) error) error {
	if s == nil || s.passwords == nil || userID <= 0 {
		return fmt.Errorf("account password store unavailable")
	}
	return s.passwords.Mutate(ctx, userID, func(settings *domain.PasswordSettings) error {
		*settings = normalizePasswordSettings(*settings)
		return apply(settings)
	})
}

func clearPassword(settings *domain.PasswordSettings) {
	loginEmail := settings.LoginEmail
	*settings = defaultPasswordSettings()
	settings.LoginEmail = loginEmail
	settings.LoginEmailPattern = emailPattern(loginEmail)
}

func applyPasswordInput(settings *domain.PasswordSettings, input domain.PasswordInputSettings) error {
	if !utf8.ValidString(input.Hint) || utf8.RuneCountInString(input.Hint) > 256 {
		return domain.ErrNewSettingsInvalid
	}

	if len(input.NewPasswordHash) == 0 && !input.HasEmail {
		clearPassword(settings)
		return nil
	}
	if len(input.NewPasswordHash) > 0 {
		if err := validateNewPasswordSettings(input); err != nil {
			return err
		}
		srpID, err := randomInt64()
		if err != nil {
			return err
		}
		verifier := padToHash(input.NewPasswordHash)
		secret, b, err := makeSRPChallenge(verifier)
		if err != nil {
			return err
		}
		algo := *input.NewAlgo
		settings.CurrentAlgo = &algo
		settings.NewAlgo = defaultPasswordAlgo()
		settings.SRPVerifier, settings.SRPBSecret, settings.SRPB = verifier, secret, b
		settings.SRPID, settings.HasPassword = srpID, true
		if input.HasHint {
			settings.Hint = input.Hint
		}
		settings.PendingResetDate = 0
	}
	if input.HasEmail {
		settings.RecoveryEmail = normalizeLoginEmail(input.Email)
		settings.HasRecovery = settings.RecoveryEmail != ""
	}
	settings.PendingChange = nil
	settings.EmailUnconfirmedPattern = ""
	return nil
}

func validRecoveryEmail(email string) bool {
	if len(email) > 254 || strings.ContainsAny(email, "\r\n") {
		return false
	}
	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email && strings.Contains(email, "@")
}

func confirmationHash(deliveryID, code string) []byte {
	sum := sha256.Sum256([]byte(deliveryID + "\x00" + code))
	return sum[:]
}

func (s *Service) newPasswordConfirmation(input domain.PasswordInputSettings, binding string) (*domain.PendingPasswordChange, string, error) {
	if s.loginEmailSender == nil {
		return nil, "", domain.ErrPasswordRecoveryNA
	}
	code, err := randomDigits(s.loginEmailCodeLength)
	if err != nil {
		return nil, "", err
	}
	deliveryID, err := otpdelivery.NewDeliveryID()
	if err != nil {
		return nil, "", err
	}
	return &domain.PendingPasswordChange{
		DeliveryID: deliveryID, Email: input.Email, Input: input, BaseBinding: binding,
		CodeHash: confirmationHash(deliveryID, code), ExpiresAt: time.Now().Add(s.loginEmailCodeTTL),
		MaxAttempts: s.loginEmailCodeMaxAttempts, CodeLength: s.loginEmailCodeLength,
	}, code, nil
}

func (s *Service) deliverPasswordConfirmation(ctx context.Context, userID int64, pending *domain.PendingPasswordChange, code string) error {
	err := deliverOTP(ctx, s.loginEmailSender, otpdelivery.Request{
		DeliveryID: pending.DeliveryID, Purpose: otpdelivery.PurposeRecoveryEmailConfirmation,
		Channel: otpdelivery.ChannelEmail, Recipient: pending.Email, Code: code, ExpiresAt: pending.ExpiresAt,
	})
	if err == nil {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	cleanupErr := s.mutatePassword(cleanupCtx, userID, func(settings *domain.PasswordSettings) error {
		if settings.PendingChange != nil && settings.PendingChange.DeliveryID == pending.DeliveryID {
			settings.PendingChange = nil
			settings.EmailUnconfirmedPattern = ""
		}
		return nil
	})
	if cleanupErr != nil {
		return fmt.Errorf("deliver recovery email confirmation: %w (cleanup: %v)", err, cleanupErr)
	}
	return fmt.Errorf("deliver recovery email confirmation: %w", err)
}

func (s *Service) UpdatePasswordSettings(ctx context.Context, userID int64, check domain.PasswordCheck, input domain.PasswordInputSettings) error {
	var pending *domain.PendingPasswordChange
	var code string
	if !utf8.ValidString(input.Hint) || utf8.RuneCountInString(input.Hint) > 256 {
		return domain.ErrNewSettingsInvalid
	}
	err := s.mutatePassword(ctx, userID, func(settings *domain.PasswordSettings) error {
		if err := checkSRP(*settings, check); err != nil {
			return err
		}
		if len(input.NewPasswordHash) > 0 {
			if err := validateNewPasswordSettings(input); err != nil {
				return err
			}
		}
		if input.HasEmail {
			input.Email = normalizeLoginEmail(input.Email)
			if input.Email != "" && !validRecoveryEmail(input.Email) {
				return domain.ErrEmailInvalid
			}
			if input.Email != "" && input.Email != settings.RecoveryEmail {
				if !settings.HasPassword && len(input.NewPasswordHash) == 0 {
					return domain.ErrNewSettingsInvalid
				}
				var err error
				pending, code, err = s.newPasswordConfirmation(input, passwordRecoveryBinding(*settings))
				if err != nil {
					return err
				}
				settings.PendingChange = pending
				settings.EmailUnconfirmedPattern = emailPattern(input.Email)
				return nil
			}
		}
		return applyPasswordInput(settings, input)
	})
	if err != nil || pending == nil {
		return err
	}
	if err := s.deliverPasswordConfirmation(ctx, userID, pending, code); err != nil {
		return err
	}
	return &domain.EmailUnconfirmedError{CodeLength: pending.CodeLength}
}

func pendingPasswordValid(settings *domain.PasswordSettings) bool {
	pending := settings.PendingChange
	return pending != nil && pending.BaseBinding == passwordRecoveryBinding(*settings) &&
		time.Now().Before(pending.ExpiresAt) && pending.Attempts < pending.MaxAttempts
}

func (s *Service) ConfirmPasswordEmail(ctx context.Context, userID int64, code string) error {
	var proofErr error
	err := s.mutatePassword(ctx, userID, func(settings *domain.PasswordSettings) error {
		if !pendingPasswordValid(settings) {
			settings.PendingChange = nil
			settings.EmailUnconfirmedPattern = ""
			proofErr = domain.ErrEmailCodeInvalid
			return nil
		}
		pending := settings.PendingChange
		if len(code) != pending.CodeLength || subtle.ConstantTimeCompare(confirmationHash(pending.DeliveryID, code), pending.CodeHash) != 1 {
			pending.Attempts++
			if pending.Attempts >= pending.MaxAttempts {
				settings.PendingChange = nil
				settings.EmailUnconfirmedPattern = ""
			}
			proofErr = domain.ErrEmailCodeInvalid
			return nil
		}
		return applyPasswordInput(settings, pending.Input)
	})
	if err != nil {
		return err
	}
	return proofErr
}

func (s *Service) ResendPasswordEmail(ctx context.Context, userID int64) error {
	var pending *domain.PendingPasswordChange
	var code string
	err := s.mutatePassword(ctx, userID, func(settings *domain.PasswordSettings) error {
		// Expiry does not discard the requested change. A fresh code replaces the
		// old proof, while factor changes invalidate the entire pending operation.
		previous := settings.PendingChange
		if previous == nil || previous.BaseBinding != passwordRecoveryBinding(*settings) {
			return domain.ErrEmailCodeInvalid
		}
		var err error
		pending, code, err = s.newPasswordConfirmation(previous.Input, previous.BaseBinding)
		if err != nil {
			return err
		}
		settings.PendingChange = pending
		return nil
	})
	if err != nil {
		return err
	}
	return s.deliverPasswordConfirmation(ctx, userID, pending, code)
}

func (s *Service) CancelPasswordEmail(ctx context.Context, userID int64) error {
	return s.mutatePassword(ctx, userID, func(settings *domain.PasswordSettings) error {
		settings.PendingChange = nil
		settings.EmailUnconfirmedPattern = ""
		return nil
	})
}

func (s *Service) RecoverPassword(ctx context.Context, userID int64, code string, input *domain.PasswordInputSettings) error {
	if s == nil || s.passwords == nil || userID <= 0 {
		return domain.ErrPasswordRecoveryExpired
	}
	return s.mutatePassword(ctx, userID, func(settings *domain.PasswordSettings) error {
		if !settings.HasPassword || settings.RecoveryEmail == "" {
			return domain.ErrPasswordRecoveryExpired
		}
		binding := passwordRecoveryBinding(*settings)
		if err := s.verifyPasswordRecoveryCode(ctx, userID, binding, code, false); err != nil {
			return err
		}
		next := *settings
		if input == nil || len(input.NewPasswordHash) == 0 {
			clearPassword(&next)
		} else {
			// Recovery changes the password, not the verified recovery address.
			value := *input
			value.HasEmail = false
			value.Email = ""
			if err := applyPasswordInput(&next, value); err != nil {
				return err
			}
		}
		if err := s.verifyPasswordRecoveryCode(ctx, userID, binding, code, true); err != nil {
			return err
		}
		*settings = next
		return nil
	})
}

func (s *Service) ResetPassword(ctx context.Context, userID int64) (domain.PasswordResetResult, error) {
	result := domain.PasswordResetResult{}
	err := s.mutatePassword(ctx, userID, func(settings *domain.PasswordSettings) error {
		if !settings.HasPassword {
			result.Kind = domain.PasswordResetOK
			return nil
		}
		if settings.HasRecovery {
			return domain.ErrPasswordRecoveryNA
		}
		now := time.Now()
		if settings.PendingResetDate > 0 && now.Unix() >= int64(settings.PendingResetDate) {
			clearPassword(settings)
			result.Kind = domain.PasswordResetOK
			return nil
		}
		if settings.PendingResetDate == 0 {
			settings.PendingResetDate = int(now.Add(passwordResetWait).Unix())
		}
		result.Kind, result.UntilDate = domain.PasswordResetRequestedWait, settings.PendingResetDate
		return nil
	})
	return result, err
}

func (s *Service) DeclinePasswordReset(ctx context.Context, userID int64) error {
	return s.mutatePassword(ctx, userID, func(settings *domain.PasswordSettings) error { settings.PendingResetDate = 0; return nil })
}
