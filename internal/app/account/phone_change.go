package account

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/store"
)

// SendChangePhoneCode creates a phone-change verification code that only the current user + perm auth_key
// may consume. CodeStore atomically rotates by purpose+user+auth_key+phone: a new request in the same
// scope immediately invalidates the old hash, so Android going back and re-entering the page does not
// leave parallel valid codes behind. SessionID is recorded for auditing but need not match at verification
// time: the same device can still complete the flow if the MTProto session is rebuilt while waiting for the
// SMS; other devices cannot reuse it because their auth_key differs.
func (s *Service) SendChangePhoneCode(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64, phone string) (string, domain.AuthCodeDelivery, error) {
	phone = domain.NormalizePhone(phone)
	if !domain.ValidPhone(phone) {
		return "", domain.AuthCodeDelivery{}, domain.ErrPhoneNumberInvalid
	}
	if _, err := s.phoneChangeCaller(ctx, userID, authKeyID); err != nil {
		return "", domain.AuthCodeDelivery{}, err
	}
	if existing, found, err := s.users.ByPhone(ctx, phone); err != nil {
		return "", domain.AuthCodeDelivery{}, err
	} else if found && existing.ID != 0 {
		return "", domain.AuthCodeDelivery{}, domain.ErrPhoneNumberOccupied
	}
	if s.codes == nil || (s.phoneCodeSender == nil && strings.TrimSpace(s.phoneChangeCode) == "") {
		return "", domain.AuthCodeDelivery{}, fmt.Errorf("phone change code service is not configured")
	}
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
	rec := store.PhoneCode{
		Version:     store.PhoneCodeVersionCurrent,
		Phone:       phone,
		Code:        code,
		DeliveryID:  deliveryID,
		Channel:     channel,
		Purpose:     store.PhoneCodePurposeChangePhone,
		UserID:      userID,
		AuthKeyID:   authKeyID,
		SessionID:   sessionID,
		MaxAttempts: s.phoneChangeMaxAttempts,
	}
	expiresAt := time.Now().Add(s.phoneChangeCodeTTL)
	if err := s.codes.Set(ctx, hash, rec, s.phoneChangeCodeTTL); err != nil {
		return "", domain.AuthCodeDelivery{}, fmt.Errorf("store phone change code: %w", err)
	}
	if s.phoneCodeSender != nil {
		if err := deliverOTP(ctx, s.phoneCodeSender, otpdelivery.Request{
			DeliveryID: deliveryID,
			Purpose:    otpdelivery.PurposeChangePhone,
			Channel:    otpdelivery.ChannelSMS,
			Recipient:  phone,
			Code:       code,
			ExpiresAt:  expiresAt,
		}); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			if _, _, cleanupErr := s.codes.ConsumeScoped(cleanupCtx, hash, rec.Scope()); cleanupErr != nil {
				return "", domain.AuthCodeDelivery{}, errors.Join(err, fmt.Errorf("rollback phone change code: %w", cleanupErr))
			}
			return "", domain.AuthCodeDelivery{}, err
		}
	}
	return hash, domain.AuthCodeDelivery{Kind: domain.AuthCodeDeliverySMS, Length: len(rec.Code)}, nil
}

// ChangePhone performs an atomic phone change after verifying the scope and code. The returned event is
// used for the current session's pts bookkeeping; other sessions receive updateUserPhone via the transactional outbox.
func (s *Service) ChangePhone(ctx context.Context, userID int64, authKeyID, originRawAuthKeyID [8]byte, sessionID int64, phone, phoneCodeHash, code string, date int) (domain.PhoneChangeResult, error) {
	if strings.TrimSpace(phoneCodeHash) == "" || strings.TrimSpace(code) == "" {
		return domain.PhoneChangeResult{}, domain.ErrPhoneCodeEmpty
	}
	phone = domain.NormalizePhone(phone)
	if !domain.ValidPhone(phone) {
		return domain.PhoneChangeResult{}, domain.ErrPhoneNumberInvalid
	}
	if _, err := s.phoneChangeCaller(ctx, userID, authKeyID); err != nil {
		return domain.PhoneChangeResult{}, err
	}
	if s.codes == nil || s.phoneChanges == nil {
		return domain.PhoneChangeResult{}, fmt.Errorf("phone change service is not configured")
	}
	scope := store.PhoneCodeScope{
		Purpose:   store.PhoneCodePurposeChangePhone,
		UserID:    userID,
		AuthKeyID: authKeyID,
		Phone:     phone,
	}
	verified, err := s.codes.VerifyScoped(ctx, phoneCodeHash, scope, strings.TrimSpace(code), s.phoneChangeMaxAttempts)
	if err != nil {
		return domain.PhoneChangeResult{}, err
	}
	switch verified.Status {
	case store.LoginCodeVerifyMissing:
		return domain.PhoneChangeResult{}, domain.ErrPhoneCodeExpired
	case store.LoginCodeVerifyInvalid:
		return domain.PhoneChangeResult{}, domain.ErrPhoneCodeInvalid
	case store.LoginCodeVerifyAccepted:
	default:
		return domain.PhoneChangeResult{}, domain.ErrPhoneCodeInvalid
	}
	if existing, occupied, err := s.users.ByPhone(ctx, phone); err != nil {
		return domain.PhoneChangeResult{}, err
	} else if occupied && existing.ID != userID {
		return domain.PhoneChangeResult{}, domain.ErrPhoneNumberOccupied
	}
	consumed := verified.Record
	if consumed.Version != store.PhoneCodeVersionCurrent || consumed.Scope() != scope ||
		(consumed.Channel != store.PhoneCodeChannelPhone && consumed.Channel != store.PhoneCodeChannelSMS) {
		return domain.PhoneChangeResult{}, domain.ErrPhoneCodeInvalid
	}
	if date == 0 {
		date = int(time.Now().Unix())
	}
	result, err := s.phoneChanges.ChangePhone(ctx, domain.PhoneChangeRequest{
		UserID: userID,
		Phone:  phone,
		Date:   date,
		// Authorization/code scope is the stable business (perm) key, while dispatch exclusion
		// must use the physical raw key.  They differ on PFS/temp connections; conflating them
		// echoes updateUserPhone back to the initiating device and suppresses the wrong session.
		ExcludeAuthKeyID: originRawAuthKeyID,
		ExcludeSessionID: sessionID,
	})
	if err != nil {
		return domain.PhoneChangeResult{}, err
	}
	if s.userCache != nil && result.User.ID != 0 {
		_ = s.userCache.Delete(ctx, []int64{result.User.ID})
	}
	return result, nil
}

func (s *Service) phoneChangeCaller(ctx context.Context, userID int64, authKeyID [8]byte) (domain.User, error) {
	if s == nil || s.users == nil || s.authorizations == nil || userID == 0 || authKeyID == ([8]byte{}) {
		return domain.User{}, domain.ErrPhoneChangeAuthInvalid
	}
	a, found, err := s.authorizations.ByAuthKey(ctx, authKeyID)
	if err != nil {
		return domain.User{}, err
	}
	if !found || a.UserID != userID || a.PasswordPending {
		return domain.User{}, domain.ErrPhoneChangeAuthInvalid
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !found {
		return domain.User{}, domain.ErrPhoneChangeAuthInvalid
	}
	if u.Bot || domain.IsSystemUserID(u.ID) {
		return domain.User{}, domain.ErrPhoneChangeForbidden
	}
	return u, nil
}

func phoneChangeHash() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate phone change hash: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
