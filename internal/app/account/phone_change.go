package account

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type reliablePhoneChangeDispatcher interface {
	UsesReliableDispatch() bool
}

func (s *Service) PhoneChangeUsesReliableDispatch() bool {
	if s == nil {
		return false
	}
	reporter, ok := s.phoneChanges.(reliablePhoneChangeDispatcher)
	return ok && reporter.UsesReliableDispatch()
}

// SendChangePhoneCode 创建只允许当前 user + perm auth_key 消费的改号验证码。
// CodeStore 会按 purpose+user+auth_key+phone 原子轮换：同一作用域的新请求
// 立即使旧 hash 失效，避免 Android 返回重进页面时留下并行有效验证码。
// SessionID 被记录用于审计，但验证时不要求相等：同一设备在等待短信期间发生
// MTProto session 重建仍可完成流程；其它设备因 auth_key 不同无法复用。
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
	if s.codes == nil || strings.TrimSpace(s.phoneChangeCode) == "" {
		return "", domain.AuthCodeDelivery{}, fmt.Errorf("phone change code service is not configured")
	}
	hash, err := phoneChangeHash()
	if err != nil {
		return "", domain.AuthCodeDelivery{}, err
	}
	rec := store.PhoneCode{
		Phone:       phone,
		Code:        s.phoneChangeCode,
		Channel:     "phone",
		Purpose:     store.PhoneCodePurposeChangePhone,
		UserID:      userID,
		AuthKeyID:   authKeyID,
		SessionID:   sessionID,
		MaxAttempts: s.phoneChangeMaxAttempts,
	}
	if err := s.codes.Set(ctx, hash, rec, s.phoneChangeCodeTTL); err != nil {
		return "", domain.AuthCodeDelivery{}, fmt.Errorf("store phone change code: %w", err)
	}
	return hash, domain.AuthCodeDelivery{Kind: domain.AuthCodeDeliverySMS, Length: len(rec.Code)}, nil
}

// ChangePhone 验证作用域和验证码后执行原子改号。返回事件用于当前 session 的
// pts 簿记；其它 session 由 transactional outbox 投递 updateUserPhone。
func (s *Service) ChangePhone(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64, phone, phoneCodeHash, code string, date int) (domain.PhoneChangeResult, error) {
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
	rec, found, err := s.codes.Get(ctx, phoneCodeHash)
	if err != nil {
		return domain.PhoneChangeResult{}, err
	}
	if !found {
		return domain.PhoneChangeResult{}, domain.ErrPhoneCodeExpired
	}
	if rec.Purpose != store.PhoneCodePurposeChangePhone || rec.Phone != phone || rec.UserID != userID || rec.AuthKeyID != authKeyID {
		return domain.PhoneChangeResult{}, domain.ErrPhoneCodeInvalid
	}
	code = strings.TrimSpace(code)
	if subtle.ConstantTimeCompare([]byte(rec.Code), []byte(code)) != 1 {
		return domain.PhoneChangeResult{}, s.rejectPhoneChangeCode(ctx, phoneCodeHash, rec)
	}
	if existing, occupied, err := s.users.ByPhone(ctx, phone); err != nil {
		return domain.PhoneChangeResult{}, err
	} else if occupied && existing.ID != userID {
		return domain.PhoneChangeResult{}, domain.ErrPhoneNumberOccupied
	}
	// 正确 code 必须在进入持久化事务前原子消费。并发重放中只有一个请求能
	// 获得记录，其余请求不得再次推进 pts 或追加 user_phone event。
	consumed, found, err := s.codes.ConsumeScoped(ctx, phoneCodeHash, store.PhoneCodeScope{
		Purpose:   store.PhoneCodePurposeChangePhone,
		UserID:    userID,
		AuthKeyID: authKeyID,
		Phone:     phone,
	})
	if err != nil {
		return domain.PhoneChangeResult{}, err
	}
	if !found {
		return domain.PhoneChangeResult{}, domain.ErrPhoneCodeExpired
	}
	if consumed.Purpose != rec.Purpose || consumed.UserID != rec.UserID || consumed.AuthKeyID != rec.AuthKeyID || consumed.Phone != rec.Phone ||
		subtle.ConstantTimeCompare([]byte(consumed.Code), []byte(code)) != 1 {
		return domain.PhoneChangeResult{}, domain.ErrPhoneCodeInvalid
	}
	if date == 0 {
		date = int(time.Now().Unix())
	}
	result, err := s.phoneChanges.ChangePhone(ctx, domain.PhoneChangeRequest{
		UserID:           userID,
		Phone:            phone,
		Date:             date,
		ExcludeAuthKeyID: authKeyID,
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

func (s *Service) rejectPhoneChangeCode(ctx context.Context, hash string, rec store.PhoneCode) error {
	rec.Attempts++
	max := rec.MaxAttempts
	if max <= 0 {
		max = s.phoneChangeMaxAttempts
	}
	if max > 0 && rec.Attempts >= max {
		_ = s.codes.Del(ctx, hash)
		return domain.ErrPhoneCodeInvalid
	}
	_ = s.codes.Update(ctx, hash, rec)
	return domain.ErrPhoneCodeInvalid
}

func phoneChangeHash() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate phone change hash: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
