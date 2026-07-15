package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
)

type authCodeRateTestService struct {
	*captureAuthService
	sendCalls   int
	resendCalls int
	resetCalls  int
	resetPhone  string
	resetHash   string
	resetUserID int64
	resetErr    error
}

func (s *authCodeRateTestService) SendCode(context.Context, string) (string, error) {
	s.sendCalls++
	return "send-hash", nil
}

func (s *authCodeRateTestService) ResendCode(context.Context, string, string) (string, error) {
	s.resendCalls++
	return "resend-hash", nil
}

func (s *authCodeRateTestService) ConsumeLoginEmailReset(_ context.Context, phone, hash string) (int64, error) {
	s.resetCalls++
	s.resetPhone = phone
	s.resetHash = hash
	return s.resetUserID, s.resetErr
}

func (s *authCodeRateTestService) SendPhoneCodeAfterLoginEmailReset(_ context.Context, _ string, expectedUserID int64) (string, error) {
	s.sendCalls++
	if expectedUserID != s.resetUserID {
		return "", auth.ErrCodeInvalid
	}
	return "send-hash", nil
}

type authCodeRateTestAccount struct {
	AccountService
	clearCalls  int
	clearUserID int64
}

func (s *authCodeRateTestAccount) ClearLoginEmail(_ context.Context, userID int64) error {
	s.clearCalls++
	s.clearUserID = userID
	return nil
}

func TestAuthSendCodeRateLimitUsesOpaquePhoneAndRawAuthKeyKeys(t *testing.T) {
	phone := "+1 (555) 123-4567"
	rawAuthKeyID := [8]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	limiter := &captureRateLimiter{}
	authService := &authCodeRateTestService{captureAuthService: &captureAuthService{}}
	r := New(Config{
		AuthCodePhoneRateLimit:   5,
		AuthCodeAuthKeyRateLimit: 20,
		AuthCodeRateWindow:       10 * time.Minute,
	}, Deps{Auth: authService, Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	ctx := WithRawAuthKeyID(context.Background(), rawAuthKeyID)
	if _, err := r.onAuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 2040}); err != nil {
		t.Fatalf("onAuthSendCode: %v", err)
	}
	if authService.sendCalls != 1 {
		t.Fatalf("SendCode calls = %d, want 1", authService.sendCalls)
	}
	if len(limiter.calls) != 2 {
		t.Fatalf("limiter calls = %d, want phone + raw auth key", len(limiter.calls))
	}
	digest := sha256.Sum256([]byte(domain.NormalizePhone(phone)))
	wantPhoneKey := authCodePhoneRateLimitKeyPrefix + hex.EncodeToString(digest[:])
	wantAuthKey := authCodeAuthKeyRateLimitKeyPrefix + hex.EncodeToString(rawAuthKeyID[:])
	if got := limiter.calls[0]; got.key != wantAuthKey || got.cost != 1 || got.limit != 20 || got.window != 10*time.Minute {
		t.Fatalf("auth-key limiter call = %+v", got)
	}
	if got := limiter.calls[1]; got.key != wantPhoneKey || got.cost != 1 || got.limit != 5 || got.window != 10*time.Minute {
		t.Fatalf("phone limiter call = %+v", got)
	}
	for _, call := range limiter.calls {
		if strings.Contains(call.key, domain.NormalizePhone(phone)) || strings.Contains(call.key, phone) {
			t.Fatalf("limiter key leaked phone: %q", call.key)
		}
	}
}

func TestAuthSendCodePhoneRateLimitPrecedesBusinessLookupAndWrite(t *testing.T) {
	limiter := &captureRateLimiter{block: true, retryAfter: 17}
	authService := &authCodeRateTestService{captureAuthService: &captureAuthService{}}
	r := New(Config{
		AuthCodePhoneRateLimit: 5,
		AuthCodeRateWindow:     time.Minute,
	}, Deps{Auth: authService, Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	_, err := r.onAuthSendCode(WithRawAuthKeyID(context.Background(), [8]byte{1}), &tg.AuthSendCodeRequest{PhoneNumber: "+86 188 0000 0000", APIID: 2040})
	if err == nil || !strings.Contains(err.Error(), "FLOOD_WAIT") || !strings.Contains(err.Error(), "(17)") {
		t.Fatalf("sendCode err = %v, want FLOOD_WAIT 17", err)
	}
	if authService.sendCalls != 0 {
		t.Fatalf("SendCode calls = %d, want 0", authService.sendCalls)
	}
	if len(authService.authKeyClientInfos) != 0 {
		t.Fatalf("blocked sendCode persisted client info: %+v", authService.authKeyClientInfos)
	}
	if len(limiter.calls) != 1 || !strings.HasPrefix(limiter.calls[0].key, authCodePhoneRateLimitKeyPrefix) {
		t.Fatalf("limiter calls = %+v, want only phone dimension", limiter.calls)
	}
}

func TestAuthSendCodeRawAuthKeyBlockDoesNotCreatePhoneDimension(t *testing.T) {
	limiter := &captureRateLimiter{block: true, retryAfter: 31}
	authService := &authCodeRateTestService{captureAuthService: &captureAuthService{}}
	r := New(Config{
		AuthCodePhoneRateLimit:   5,
		AuthCodeAuthKeyRateLimit: 20,
		AuthCodeRateWindow:       time.Minute,
	}, Deps{Auth: authService, Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	rawAuthKeyID := [8]byte{7, 7, 7, 7, 7, 7, 7, 7}
	_, err := r.onAuthSendCode(WithRawAuthKeyID(context.Background(), rawAuthKeyID), &tg.AuthSendCodeRequest{PhoneNumber: "15550000001"})
	if err == nil || !strings.Contains(err.Error(), "FLOOD_WAIT") {
		t.Fatalf("sendCode err = %v, want FLOOD_WAIT", err)
	}
	wantKey := authCodeAuthKeyRateLimitKeyPrefix + hex.EncodeToString(rawAuthKeyID[:])
	if len(limiter.calls) != 1 || limiter.calls[0].key != wantKey {
		t.Fatalf("limiter calls = %+v, want only raw auth-key %q", limiter.calls, wantKey)
	}
	if authService.sendCalls != 0 {
		t.Fatalf("SendCode calls = %d, want 0", authService.sendCalls)
	}
}

func TestAuthSendCodeInvalidPhoneCreatesNoLimiterKey(t *testing.T) {
	limiter := &captureRateLimiter{}
	authService := &authCodeRateTestService{captureAuthService: &captureAuthService{}}
	r := New(Config{
		AuthCodePhoneRateLimit:   5,
		AuthCodeAuthKeyRateLimit: 20,
		AuthCodeRateWindow:       time.Minute,
	}, Deps{Auth: authService, Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	_, err := r.onAuthSendCode(WithRawAuthKeyID(context.Background(), [8]byte{1}), &tg.AuthSendCodeRequest{PhoneNumber: "not-a-phone"})
	if err == nil || !strings.Contains(err.Error(), "PHONE_NUMBER_INVALID") {
		t.Fatalf("sendCode err = %v, want PHONE_NUMBER_INVALID", err)
	}
	if len(limiter.calls) != 0 || authService.sendCalls != 0 {
		t.Fatalf("invalid phone limiter/service calls = %d/%d, want 0/0", len(limiter.calls), authService.sendCalls)
	}
}

func TestAuthResendCodeRawAuthKeyRateLimitPrecedesRotation(t *testing.T) {
	limiter := &captureRateLimiter{block: true, retryAfter: 23}
	authService := &authCodeRateTestService{captureAuthService: &captureAuthService{}}
	r := New(Config{
		AuthCodeAuthKeyRateLimit: 20,
		AuthCodeRateWindow:       2 * time.Minute,
	}, Deps{Auth: authService, Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	rawAuthKeyID := [8]byte{9, 8, 7, 6, 5, 4, 3, 2}
	_, err := r.onAuthResendCode(WithRawAuthKeyID(context.Background(), rawAuthKeyID), &tg.AuthResendCodeRequest{
		PhoneNumber:   "8618800000000",
		PhoneCodeHash: "old-hash",
	})
	if err == nil || !strings.Contains(err.Error(), "FLOOD_WAIT") || !strings.Contains(err.Error(), "(23)") {
		t.Fatalf("resendCode err = %v, want FLOOD_WAIT 23", err)
	}
	if authService.resendCalls != 0 {
		t.Fatalf("ResendCode calls = %d, want 0", authService.resendCalls)
	}
	wantKey := authCodeAuthKeyRateLimitKeyPrefix + hex.EncodeToString(rawAuthKeyID[:])
	if len(limiter.calls) != 1 || limiter.calls[0].key != wantKey {
		t.Fatalf("limiter calls = %+v, want %q", limiter.calls, wantKey)
	}
}

func TestAuthResetLoginEmailRateLimitPrecedesEmailClear(t *testing.T) {
	limiter := &captureRateLimiter{block: true, retryAfter: 29}
	authService := &authCodeRateTestService{captureAuthService: &captureAuthService{}}
	accountService := &authCodeRateTestAccount{}
	r := New(Config{AuthCodePhoneRateLimit: 5, AuthCodeRateWindow: time.Minute}, Deps{
		Auth: authService, Account: accountService, Limiter: limiter,
	}, zaptest.NewLogger(t), clock.System)

	_, err := r.onAuthResetLoginEmail(context.Background(), &tg.AuthResetLoginEmailRequest{
		PhoneNumber:   "8618800000000",
		PhoneCodeHash: "email-hash",
	})
	if err == nil || !strings.Contains(err.Error(), "FLOOD_WAIT") || !strings.Contains(err.Error(), "(29)") {
		t.Fatalf("resetLoginEmail err = %v, want FLOOD_WAIT 29", err)
	}
	if accountService.clearCalls != 0 || authService.resetCalls != 0 || authService.sendCalls != 0 {
		t.Fatalf("side effects reset=%d clear=%d send=%d, want 0/0/0", authService.resetCalls, accountService.clearCalls, authService.sendCalls)
	}
}

func TestAuthResetLoginEmailConsumesHashBeforeClearAndResend(t *testing.T) {
	authService := &authCodeRateTestService{captureAuthService: &captureAuthService{}, resetUserID: 4242}
	accountService := &authCodeRateTestAccount{}
	r := New(Config{}, Deps{Auth: authService, Account: accountService}, zaptest.NewLogger(t), clock.System)
	req := &tg.AuthResetLoginEmailRequest{PhoneNumber: "+1 555 123 9999", PhoneCodeHash: "email-login-hash"}

	result, err := r.onAuthResetLoginEmail(context.Background(), req)
	if err != nil {
		t.Fatalf("onAuthResetLoginEmail: %v", err)
	}
	if authService.resetCalls != 1 || authService.resetPhone != req.PhoneNumber || authService.resetHash != req.PhoneCodeHash ||
		accountService.clearCalls != 1 || accountService.clearUserID != authService.resetUserID || authService.sendCalls != 1 {
		t.Fatalf("calls reset=%d(%q,%q uid=%d) clear=%d(uid=%d) send=%d", authService.resetCalls, authService.resetPhone, authService.resetHash, authService.resetUserID, accountService.clearCalls, accountService.clearUserID, authService.sendCalls)
	}
	sent, ok := result.(*tg.AuthSentCode)
	if !ok || sent.PhoneCodeHash != "send-hash" {
		t.Fatalf("result=%T %+v, want sentCode/send-hash", result, result)
	}
}

func TestAuthResetLoginEmailInvalidHashNeverClearsFactor(t *testing.T) {
	authService := &authCodeRateTestService{captureAuthService: &captureAuthService{}, resetErr: auth.ErrCodeExpired}
	accountService := &authCodeRateTestAccount{}
	r := New(Config{}, Deps{Auth: authService, Account: accountService}, zaptest.NewLogger(t), clock.System)

	_, err := r.onAuthResetLoginEmail(context.Background(), &tg.AuthResetLoginEmailRequest{
		PhoneNumber:   "15551239999",
		PhoneCodeHash: "expired-email-hash",
	})
	if err == nil || !strings.Contains(err.Error(), "PHONE_CODE_EXPIRED") {
		t.Fatalf("onAuthResetLoginEmail err=%v, want PHONE_CODE_EXPIRED", err)
	}
	if authService.resetCalls != 1 || accountService.clearCalls != 0 || authService.sendCalls != 0 {
		t.Fatalf("calls reset=%d clear=%d send=%d, want 1/0/0", authService.resetCalls, accountService.clearCalls, authService.sendCalls)
	}
}
