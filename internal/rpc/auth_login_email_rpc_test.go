package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type loginEmailAccountService struct {
	AccountService
	verifiedEmail string
}

func (s loginEmailAccountService) VerifyLoginEmail(context.Context, int64, string, string, string, bool) (string, error) {
	return s.verifiedEmail, nil
}

func TestEmailSentCodeUsesDeliveryLength(t *testing.T) {
	authSvc := &captureAuthService{
		codeDelivery: domain.AuthCodeDelivery{
			Kind:         domain.AuthCodeDeliveryEmail,
			EmailPattern: "a***e@example.test",
			Length:       6,
		},
	}
	r := New(Config{}, Deps{Auth: authSvc}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000000, 0)})

	sent, err := r.tgSentCodeForHash(context.Background(), "hash-email")
	if err != nil {
		t.Fatalf("tgSentCodeForHash: %v", err)
	}
	code, ok := sent.(*tg.AuthSentCode)
	if !ok {
		t.Fatalf("sent = %T, want *tg.AuthSentCode", sent)
	}
	emailType, ok := code.Type.(*tg.AuthSentCodeTypeEmailCode)
	if !ok {
		t.Fatalf("sent type = %T, want *tg.AuthSentCodeTypeEmailCode", code.Type)
	}
	if emailType.Length != 6 {
		t.Fatalf("email sent code length = %d, want 6", emailType.Length)
	}
}

func TestAccountVerifyEmailLoginSetupReturnsSentCodeSuccess(t *testing.T) {
	user := domain.User{
		ID:         100200300,
		AccessHash: 900100200,
		Phone:      "8618800000020",
		FirstName:  "Alice",
	}
	authSvc := &captureAuthService{signInUser: user}
	r := New(Config{}, Deps{
		Auth:    authSvc,
		Account: loginEmailAccountService{verifiedEmail: "alice@example.test"},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000000, 0)})

	got, err := r.onAccountVerifyEmail(context.Background(), &tg.AccountVerifyEmailRequest{
		Purpose: &tg.EmailVerifyPurposeLoginSetup{
			PhoneNumber:   "+86 188 0000 0020",
			PhoneCodeHash: "hash-email-setup",
		},
		Verification: &tg.EmailVerificationCode{Code: "654321"},
	})
	if err != nil {
		t.Fatalf("onAccountVerifyEmail: %v", err)
	}
	verified, ok := got.(*tg.AccountEmailVerifiedLogin)
	if !ok {
		t.Fatalf("verified = %T, want *tg.AccountEmailVerifiedLogin", got)
	}
	if verified.Email != "alice@example.test" {
		t.Fatalf("verified email = %q", verified.Email)
	}
	success, ok := verified.SentCode.(*tg.AuthSentCodeSuccess)
	if !ok {
		t.Fatalf("sent code = %T, want *tg.AuthSentCodeSuccess", verified.SentCode)
	}
	authorization, ok := success.Authorization.(*tg.AuthAuthorization)
	if !ok {
		t.Fatalf("authorization = %T, want *tg.AuthAuthorization", success.Authorization)
	}
	self, ok := authorization.User.(*tg.User)
	if !ok {
		t.Fatalf("authorization user = %T, want *tg.User", authorization.User)
	}
	if self.ID != user.ID || !self.Self {
		t.Fatalf("authorization user = %+v, want self user %d", self, user.ID)
	}
	if authSvc.signInWithEmailCount != 1 {
		t.Fatalf("SignInWithEmail calls = %d, want 1", authSvc.signInWithEmailCount)
	}
}

func TestAccountVerifyEmailLoginSetupAndroidReturnsEmailSentCode(t *testing.T) {
	authSvc := &captureAuthService{}
	r := New(Config{}, Deps{
		Auth:    authSvc,
		Account: loginEmailAccountService{verifiedEmail: "alice@example.test"},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000000, 0)})

	ctx := WithClientInfo(context.Background(), ClientInfo{
		Type:       ClientTypeAndroid,
		AppVersion: "12.8.1 (69169) pbeta",
	})
	got, err := r.onAccountVerifyEmail(ctx, &tg.AccountVerifyEmailRequest{
		Purpose: &tg.EmailVerifyPurposeLoginSetup{
			PhoneNumber:   "+86 188 0000 0020",
			PhoneCodeHash: "hash-email-setup",
		},
		Verification: &tg.EmailVerificationCode{Code: "654321"},
	})
	if err != nil {
		t.Fatalf("onAccountVerifyEmail: %v", err)
	}
	verified, ok := got.(*tg.AccountEmailVerifiedLogin)
	if !ok {
		t.Fatalf("verified = %T, want *tg.AccountEmailVerifiedLogin", got)
	}
	sent, ok := verified.SentCode.(*tg.AuthSentCode)
	if !ok {
		t.Fatalf("sent code = %T, want *tg.AuthSentCode", verified.SentCode)
	}
	if sent.PhoneCodeHash != "hash-email-setup" {
		t.Fatalf("phone_code_hash = %q", sent.PhoneCodeHash)
	}
	emailType, ok := sent.Type.(*tg.AuthSentCodeTypeEmailCode)
	if !ok {
		t.Fatalf("sent type = %T, want *tg.AuthSentCodeTypeEmailCode", sent.Type)
	}
	if emailType.EmailPattern != "a***e@example.test" {
		t.Fatalf("email pattern = %q", emailType.EmailPattern)
	}
	if emailType.Length != 6 {
		t.Fatalf("email code length = %d, want 6", emailType.Length)
	}
	if authSvc.signInWithEmailCount != 0 {
		t.Fatalf("SignInWithEmail calls = %d, want 0 for Android compat downgrade", authSvc.signInWithEmailCount)
	}
}
