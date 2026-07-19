package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/store"
)

const accountDeletionDelay = 7 * 24 * time.Hour

// DeleteAccount implements the official 2FA deletion decision. A supplied and
// valid SRP proof always deletes immediately. Without a proof, an account whose
// password is older than seven days and which was active during the last seven
// days gets a cancellable seven-day window; all other cases delete immediately.
func (s *Service) DeleteAccount(ctx context.Context, userID int64, authKeyID [8]byte, reason string, password *domain.PasswordCheck, now time.Time) (domain.AccountDeleteOutcome, error) {
	if s == nil || s.lifecycle == nil || userID == 0 || authKeyID == ([8]byte{}) {
		return domain.AccountDeleteOutcome{}, domain.ErrAccountDeletionForbidden
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 1024 {
		return domain.AccountDeleteOutcome{}, domain.ErrAccountDeletionForbidden
	}
	snapshot, found, err := s.lifecycle.AccountDeletionSnapshot(ctx, userID)
	if err != nil {
		return domain.AccountDeleteOutcome{}, err
	}
	if !found {
		return domain.AccountDeleteOutcome{}, domain.ErrUserNotFound
	}
	if snapshot.User.Deleted {
		return domain.AccountDeleteOutcome{Kind: domain.AccountDeleteImmediate, Deletion: domain.AccountDeletionResult{User: snapshot.User}}, nil
	}
	if snapshot.User.Bot || domain.IsSystemUserID(snapshot.User.ID) {
		return domain.AccountDeleteOutcome{}, domain.ErrAccountDeletionForbidden
	}
	if password != nil && !password.Empty {
		if !snapshot.HasPassword {
			return domain.AccountDeleteOutcome{}, domain.ErrPasswordHashInvalid
		}
		if err := s.CheckPassword(ctx, userID, *password); err != nil {
			return domain.AccountDeleteOutcome{}, err
		}
		return s.executeAccountDeletion(ctx, userID, deletionSourceForReason(reason), reason, now)
	}
	if !snapshot.HasPassword {
		return s.executeAccountDeletion(ctx, userID, deletionSourceForReason(reason), reason, now)
	}
	lastActive := snapshot.User.CreatedAt
	if snapshot.User.LastSeenAt > 0 {
		seen := time.Unix(int64(snapshot.User.LastSeenAt), 0).UTC()
		if seen.After(lastActive) {
			lastActive = seen
		}
	}
	passwordOldEnough := !snapshot.PasswordUpdatedAt.IsZero() && !snapshot.PasswordUpdatedAt.After(now.Add(-accountDeletionDelay))
	recentlyActive := !lastActive.IsZero() && !lastActive.Before(now.Add(-accountDeletionDelay))
	if !passwordOldEnough || !recentlyActive {
		return s.executeAccountDeletion(ctx, userID, deletionSourceForReason(reason), reason, now)
	}
	if snapshot.Pending != nil {
		return delayedDeleteOutcome(*snapshot.Pending, now), nil
	}
	rawToken, digest, err := newAccountDeletionToken()
	if err != nil {
		return domain.AccountDeleteOutcome{}, err
	}
	executeAt := now.Add(accountDeletionDelay)
	message := fmt.Sprintf(
		"A request was made to delete your Telegram account. If this wasn't you, cancel the request: tg://confirmphone?phone=%s&hash=%s",
		url.QueryEscape(snapshot.User.Phone), url.QueryEscape(rawToken),
	)
	pending, _, err := s.lifecycle.ScheduleAccountDeletion(ctx, domain.ScheduleAccountDeletion{
		UserID:             userID,
		RequesterAuthKeyID: authKeyID,
		Reason:             reason,
		ConfirmHashDigest:  digest,
		ServiceMessage:     message,
		RequestedAt:        now,
		ExecuteAt:          executeAt,
	})
	if err != nil {
		return domain.AccountDeleteOutcome{}, err
	}
	return delayedDeleteOutcome(pending, now), nil
}

func (s *Service) executeAccountDeletion(ctx context.Context, userID int64, source domain.AccountDeletionSource, reason string, now time.Time) (domain.AccountDeleteOutcome, error) {
	result, err := s.lifecycle.ExecuteAccountDeletion(ctx, userID, source, reason, now)
	if err != nil {
		return domain.AccountDeleteOutcome{}, err
	}
	if s.userCache != nil {
		_ = s.userCache.Delete(ctx, []int64{userID})
	}
	return domain.AccountDeleteOutcome{Kind: domain.AccountDeleteImmediate, Deletion: result}, nil
}

func delayedDeleteOutcome(pending domain.AccountDeletionRequest, now time.Time) domain.AccountDeleteOutcome {
	wait := int(time.Until(pending.ExecuteAt).Seconds())
	if !now.IsZero() {
		wait = int(pending.ExecuteAt.Sub(now).Seconds())
	}
	if wait < 0 {
		wait = 0
	}
	return domain.AccountDeleteOutcome{Kind: domain.AccountDeleteDelayed, WaitSeconds: wait, ExecuteAt: pending.ExecuteAt}
}

func deletionSourceForReason(reason string) domain.AccountDeletionSource {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "forgot password":
		return domain.AccountDeletionForgotPassword
	case "decline tos update":
		return domain.AccountDeletionTOSDecline
	default:
		return domain.AccountDeletionManual
	}
}

func newAccountDeletionToken() (string, [32]byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", [32]byte{}, fmt.Errorf("generate account deletion token: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	return token, sha256.Sum256([]byte(token)), nil
}

func accountDeletionDigest(raw string) ([32]byte, error) {
	raw = strings.TrimSpace(raw)
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		return [32]byte{}, domain.ErrAccountDeletionHashInvalid
	}
	return sha256.Sum256([]byte(raw)), nil
}

// SendConfirmPhoneCode validates the secret confirmphone link and issues an
// auth-key-scoped SMS code to the account's current phone.
func (s *Service) SendConfirmPhoneCode(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64, rawHash string) (string, domain.AuthCodeDelivery, error) {
	digest, err := accountDeletionDigest(rawHash)
	if err != nil {
		return "", domain.AuthCodeDelivery{}, err
	}
	if s == nil || s.lifecycle == nil || s.users == nil || s.codes == nil || userID == 0 || authKeyID == ([8]byte{}) {
		return "", domain.AuthCodeDelivery{}, domain.ErrAccountDeletionHashInvalid
	}
	if _, found, err := s.lifecycle.PendingAccountDeletionByHash(ctx, userID, digest); err != nil {
		return "", domain.AuthCodeDelivery{}, err
	} else if !found {
		return "", domain.AuthCodeDelivery{}, domain.ErrAccountDeletionHashInvalid
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return "", domain.AuthCodeDelivery{}, err
	}
	if !found || u.Deleted || u.Phone == "" {
		return "", domain.AuthCodeDelivery{}, domain.ErrAccountDeletionHashInvalid
	}
	return s.issueConfirmPhoneCode(ctx, userID, authKeyID, sessionID, u.Phone, digest)
}

func (s *Service) issueConfirmPhoneCode(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64, phone string, digest [32]byte) (string, domain.AuthCodeDelivery, error) {
	hash, err := phoneChangeHash()
	if err != nil {
		return "", domain.AuthCodeDelivery{}, err
	}
	code := s.phoneChangeCode
	channel := store.PhoneCodeChannelPhone
	deliveryID := ""
	if s.phoneCodeSender != nil {
		code, err = randomDigits(s.phoneCodeLength)
		if err != nil {
			return "", domain.AuthCodeDelivery{}, err
		}
		deliveryID, err = otpdelivery.NewDeliveryID()
		if err != nil {
			return "", domain.AuthCodeDelivery{}, err
		}
		channel = store.PhoneCodeChannelSMS
	}
	if strings.TrimSpace(code) == "" {
		return "", domain.AuthCodeDelivery{}, fmt.Errorf("confirm phone code service is not configured")
	}
	rec := store.PhoneCode{
		Version:             store.PhoneCodeVersionCurrent,
		Phone:               phone,
		Code:                code,
		DeliveryID:          deliveryID,
		Channel:             channel,
		Purpose:             store.PhoneCodePurposeConfirmPhone,
		UserID:              userID,
		AuthKeyID:           authKeyID,
		SessionID:           sessionID,
		MaxAttempts:         s.phoneChangeMaxAttempts,
		AccountDeletionHash: hex.EncodeToString(digest[:]),
	}
	expiresAt := time.Now().Add(s.phoneChangeCodeTTL)
	if err := s.codes.Set(ctx, hash, rec, s.phoneChangeCodeTTL); err != nil {
		return "", domain.AuthCodeDelivery{}, fmt.Errorf("store confirm phone code: %w", err)
	}
	if s.phoneCodeSender != nil {
		if err := deliverOTP(ctx, s.phoneCodeSender, otpdelivery.Request{
			DeliveryID: deliveryID,
			Purpose:    otpdelivery.PurposeConfirmPhone,
			Channel:    otpdelivery.ChannelSMS,
			Recipient:  phone,
			Code:       code,
			ExpiresAt:  expiresAt,
		}); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			if _, _, cleanupErr := s.codes.ConsumeScoped(cleanupCtx, hash, rec.Scope()); cleanupErr != nil {
				return "", domain.AuthCodeDelivery{}, errors.Join(err, cleanupErr)
			}
			return "", domain.AuthCodeDelivery{}, err
		}
	}
	return hash, domain.AuthCodeDelivery{Kind: domain.AuthCodeDeliverySMS, Length: len(code)}, nil
}

// ConfirmPhone consumes the scoped OTP, cancels the pending deletion and
// revokes the auth key that initiated the deletion attempt.
func (s *Service) ConfirmPhone(ctx context.Context, userID int64, authKeyID [8]byte, phoneCodeHash, code string, now time.Time) ([]domain.Authorization, error) {
	if strings.TrimSpace(phoneCodeHash) == "" || strings.TrimSpace(code) == "" {
		return nil, domain.ErrPhoneCodeEmpty
	}
	if s == nil || s.codes == nil || s.lifecycle == nil || s.users == nil {
		return nil, domain.ErrPhoneCodeInvalid
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !found || u.Deleted || u.Phone == "" {
		return nil, domain.ErrPhoneCodeInvalid
	}
	scope := store.PhoneCodeScope{Purpose: store.PhoneCodePurposeConfirmPhone, UserID: userID, AuthKeyID: authKeyID, Phone: u.Phone}
	verified, err := s.codes.VerifyScoped(ctx, phoneCodeHash, scope, strings.TrimSpace(code), s.phoneChangeMaxAttempts)
	if err != nil {
		return nil, err
	}
	switch verified.Status {
	case store.LoginCodeVerifyMissing:
		return nil, domain.ErrPhoneCodeExpired
	case store.LoginCodeVerifyInvalid:
		return nil, domain.ErrPhoneCodeInvalid
	case store.LoginCodeVerifyAccepted:
	default:
		return nil, domain.ErrPhoneCodeInvalid
	}
	digestBytes, err := hex.DecodeString(verified.Record.AccountDeletionHash)
	if err != nil || len(digestBytes) != 32 {
		return nil, domain.ErrPhoneCodeInvalid
	}
	var digest [32]byte
	copy(digest[:], digestBytes)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.lifecycle.CancelAccountDeletion(ctx, userID, digest, now)
}

// ResendConfirmPhoneCode handles auth.resendCode only when the supplied hash is
// an active confirm-phone code for this authorized user/auth key.
func (s *Service) ResendConfirmPhoneCode(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64, phone, oldHash string) (string, domain.AuthCodeDelivery, bool, error) {
	if s == nil || s.codes == nil || s.users == nil || userID == 0 {
		return "", domain.AuthCodeDelivery{}, false, nil
	}
	rec, found, err := s.codes.Get(ctx, oldHash)
	if err != nil || !found || rec.Purpose != store.PhoneCodePurposeConfirmPhone || rec.UserID != userID || rec.AuthKeyID != authKeyID {
		return "", domain.AuthCodeDelivery{}, false, err
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil || !found || u.Deleted || domain.NormalizePhone(phone) != domain.NormalizePhone(u.Phone) {
		if err == nil {
			err = domain.ErrPhoneCodeInvalid
		}
		return "", domain.AuthCodeDelivery{}, true, err
	}
	consumed, found, err := s.codes.ConsumeScoped(ctx, oldHash, rec.Scope())
	if err != nil || !found {
		if err == nil {
			err = domain.ErrPhoneCodeExpired
		}
		return "", domain.AuthCodeDelivery{}, true, err
	}
	digestBytes, err := hex.DecodeString(consumed.AccountDeletionHash)
	if err != nil || len(digestBytes) != 32 {
		return "", domain.AuthCodeDelivery{}, true, domain.ErrPhoneCodeInvalid
	}
	var digest [32]byte
	copy(digest[:], digestBytes)
	hash, delivery, err := s.issueConfirmPhoneCode(ctx, userID, authKeyID, sessionID, u.Phone, digest)
	return hash, delivery, true, err
}

func (s *Service) CancelConfirmPhoneCode(ctx context.Context, userID int64, authKeyID [8]byte, phone, hash string) (bool, error) {
	if s == nil || s.codes == nil || userID == 0 {
		return false, nil
	}
	rec, found, err := s.codes.Get(ctx, hash)
	if err != nil || !found || rec.Purpose != store.PhoneCodePurposeConfirmPhone || rec.UserID != userID || rec.AuthKeyID != authKeyID {
		return false, err
	}
	if domain.NormalizePhone(phone) != domain.NormalizePhone(rec.Phone) {
		return true, domain.ErrPhoneCodeInvalid
	}
	_, _, err = s.codes.ConsumeScoped(ctx, hash, rec.Scope())
	return true, err
}

func (s *Service) SweepDueAccountDeletions(ctx context.Context, now time.Time, limit int) ([]domain.AccountDeletionResult, error) {
	if s == nil || s.lifecycle == nil || limit <= 0 {
		return nil, nil
	}
	candidates, err := s.lifecycle.DueAccountDeletions(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	out := make([]domain.AccountDeletionResult, 0, len(candidates))
	for _, candidate := range candidates {
		result, err := s.lifecycle.ExecuteAccountDeletion(ctx, candidate.UserID, candidate.Source, "", now)
		if err != nil {
			return out, err
		}
		if s.userCache != nil {
			_ = s.userCache.Delete(ctx, []int64{candidate.UserID})
		}
		out = append(out, result)
	}
	return out, nil
}

func (s *Service) ClaimAccountDeletionNotifications(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.AccountDeletionNotification, error) {
	if s == nil || s.lifecycle == nil {
		return nil, nil
	}
	return s.lifecycle.ClaimAccountDeletionNotifications(ctx, now, limit, lease)
}

func (s *Service) CompleteAccountDeletionNotification(ctx context.Context, id int64, now time.Time) error {
	if s == nil || s.lifecycle == nil {
		return nil
	}
	return s.lifecycle.CompleteAccountDeletionNotification(ctx, id, now)
}
