package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func newLoginEmailService(t *testing.T) (*Service, *memory.UserStore) {
	t.Helper()
	users := memory.NewUserStore()
	svc := NewService(memory.NewPasswordStore(), WithUsers(users))
	return svc, users
}

func createUser(t *testing.T, users *memory.UserStore, phone string) domain.User {
	t.Helper()
	u, err := users.Create(context.Background(), domain.User{Phone: phone, FirstName: "Test"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

type captureMailSender struct {
	to   string
	code string
}

func (s *captureMailSender) SendLoginCode(_ context.Context, to, code string, _ time.Duration) error {
	s.to = to
	s.code = code
	return nil
}

// TestSetLoginEmailPersistsAndMasks 设置登录邮箱后，GetPassword 下发掩码 pattern，原始
// 地址只在 LoginEmail 读路径可见。
func TestSetLoginEmailPersistsAndMasks(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginEmailService(t)
	u := createUser(t, users, "15550010001")

	if err := svc.SetLoginEmail(ctx, u.ID, "alice@example.com"); err != nil {
		t.Fatalf("SetLoginEmail: %v", err)
	}

	settings, err := svc.GetPassword(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetPassword: %v", err)
	}
	if got, want := settings.LoginEmailPattern, "a***e@example.com"; got != want {
		t.Fatalf("LoginEmailPattern = %q, want %q", got, want)
	}
	if settings.LoginEmail != "alice@example.com" {
		t.Fatalf("LoginEmail = %q, want raw address", settings.LoginEmail)
	}

	email, found, err := svc.LoginEmail(ctx, u.ID)
	if err != nil || !found || email != "alice@example.com" {
		t.Fatalf("LoginEmail = %q found=%v err=%v", email, found, err)
	}
}

// TestLoginEmailByPhoneAndClear 验证按手机号读取/清除登录邮箱（sendCode 检测 + reset 用）。
func TestLoginEmailByPhoneAndClear(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginEmailService(t)
	createUser(t, users, "15550010002")

	if err := svc.SetLoginEmailByPhone(ctx, "+1 555 001 0002", "bob@mail.com"); err != nil {
		t.Fatalf("SetLoginEmailByPhone: %v", err)
	}
	email, found, err := svc.LoginEmailByPhone(ctx, "15550010002")
	if err != nil || !found || email != "bob@mail.com" {
		t.Fatalf("LoginEmailByPhone = %q found=%v err=%v", email, found, err)
	}

	if err := svc.ClearLoginEmailByPhone(ctx, "15550010002"); err != nil {
		t.Fatalf("ClearLoginEmailByPhone: %v", err)
	}
	if _, found, _ := svc.LoginEmailByPhone(ctx, "15550010002"); found {
		t.Fatal("login email still present after clear")
	}
}

// TestSetLoginEmailRejectsInvalid 空/无 @ 的邮箱被拒。
func TestSetLoginEmailRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginEmailService(t)
	u := createUser(t, users, "15550010003")

	for _, bad := range []string{"", "   ", "not-an-email"} {
		if err := svc.SetLoginEmail(ctx, u.ID, bad); !errors.Is(err, domain.ErrEmailInvalid) {
			t.Fatalf("SetLoginEmail(%q) err = %v, want ErrEmailInvalid", bad, err)
		}
	}
}

func TestSetLoginEmailRejectsDuplicateCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginEmailService(t)
	u1 := createUser(t, users, "15550010103")
	u2 := createUser(t, users, "15550010104")

	if err := svc.SetLoginEmail(ctx, u1.ID, "Alice@Example.Test"); err != nil {
		t.Fatalf("SetLoginEmail user1: %v", err)
	}
	if err := svc.SetLoginEmail(ctx, u2.ID, "alice@example.test"); !errors.Is(err, domain.ErrEmailOccupied) {
		t.Fatalf("SetLoginEmail duplicate err = %v, want ErrEmailOccupied", err)
	}
	email, found, err := svc.LoginEmail(ctx, u1.ID)
	if err != nil || !found || email != "alice@example.test" {
		t.Fatalf("LoginEmail user1 = %q found=%v err=%v", email, found, err)
	}
}

// TestRecoveryEmailDoesNotLeakIntoLoginEmailPattern 是核心解耦回归：设置 2FA 恢复邮箱
// 不得把恢复邮箱掩码写进 login_email_pattern（历史 bug）。
func TestRecoveryEmailDoesNotLeakIntoLoginEmailPattern(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginEmailService(t)
	u := createUser(t, users, "15550010004")

	// 设置 2FA 恢复邮箱（email-only 路径即可触发历史 bug 的写入点）。
	if err := svc.UpdatePasswordSettings(ctx, u.ID, domain.PasswordCheck{Empty: true}, domain.PasswordInputSettings{
		Email:    "recovery@secret.com",
		HasEmail: true,
	}); err != nil {
		t.Fatalf("UpdatePasswordSettings: %v", err)
	}

	settings, err := svc.GetPassword(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetPassword: %v", err)
	}
	if settings.LoginEmailPattern != "" {
		t.Fatalf("LoginEmailPattern = %q, want empty (recovery email must not leak into login email)", settings.LoginEmailPattern)
	}
	if !settings.HasRecovery {
		t.Fatal("HasRecovery = false, want true after setting recovery email")
	}
}

func TestSendLoginEmailCodeRejectsDuplicateBeforeSending(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	passwords := memory.NewPasswordStore()
	sender := &captureMailSender{}
	svc := NewService(passwords,
		WithUsers(users),
		WithLoginEmailVerification(codes, sender, time.Minute, 2, 6))
	u1 := createUser(t, users, "15550010105")
	u2 := createUser(t, users, "15550010106")
	if err := svc.SetLoginEmail(ctx, u1.ID, "taken@example.test"); err != nil {
		t.Fatalf("SetLoginEmail user1: %v", err)
	}

	if _, _, err := svc.SendLoginEmailCode(ctx, u2.ID, "", "", "TAKEN@example.test", false); !errors.Is(err, domain.ErrEmailOccupied) {
		t.Fatalf("SendLoginEmailCode duplicate err = %v, want ErrEmailOccupied", err)
	}
	if sender.to != "" || sender.code != "" {
		t.Fatalf("duplicate email sent to=%q code=%q, want no send", sender.to, sender.code)
	}
}

func TestLoginEmailSetupRejectsAlreadyOwnedEmailForNewPhone(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	passwords := memory.NewPasswordStore()
	sender := &captureMailSender{}
	svc := NewService(passwords,
		WithUsers(users),
		WithLoginEmailVerification(codes, sender, time.Minute, 2, 6))
	owner := createUser(t, users, "15550010107")
	if err := svc.SetLoginEmail(ctx, owner.ID, "owner@example.test"); err != nil {
		t.Fatalf("SetLoginEmail owner: %v", err)
	}
	if err := codes.Set(ctx, "new-phone-hash", store.PhoneCode{Phone: "15550010108", Channel: "email_setup_required", MaxAttempts: 2}, time.Minute); err != nil {
		t.Fatalf("seed phone code: %v", err)
	}

	if _, _, err := svc.SendLoginEmailCode(ctx, 0, "+1 555 001 0108", "new-phone-hash", "OWNER@example.test", true); !errors.Is(err, domain.ErrEmailOccupied) {
		t.Fatalf("setup duplicate email err = %v, want ErrEmailOccupied", err)
	}
	if sender.to != "" || sender.code != "" {
		t.Fatalf("duplicate setup email sent to=%q code=%q, want no send", sender.to, sender.code)
	}
}

func TestSendVerifyLoginEmailPersistsOnlyAfterVerify(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	sender := &captureMailSender{}
	svc := NewService(memory.NewPasswordStore(),
		WithUsers(users),
		WithLoginEmailVerification(codes, sender, time.Minute, 2, 6))
	u := createUser(t, users, "15550010005")

	pattern, length, err := svc.SendLoginEmailCode(ctx, u.ID, "", "", "alice@example.test", false)
	if err != nil {
		t.Fatalf("SendLoginEmailCode: %v", err)
	}
	if pattern != "a***e@example.test" || length != 6 || sender.to != "alice@example.test" || len(sender.code) != 6 {
		t.Fatalf("send result pattern=%q length=%d to=%q code=%q", pattern, length, sender.to, sender.code)
	}
	if _, found, err := svc.LoginEmail(ctx, u.ID); err != nil || found {
		t.Fatalf("LoginEmail before verify found=%v err=%v, want not found", found, err)
	}
	email, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", sender.code, false)
	if err != nil {
		t.Fatalf("VerifyLoginEmail: %v", err)
	}
	if email != "alice@example.test" {
		t.Fatalf("verified email = %q", email)
	}
	got, found, err := svc.LoginEmail(ctx, u.ID)
	if err != nil || !found || got != "alice@example.test" {
		t.Fatalf("LoginEmail after verify = %q found=%v err=%v", got, found, err)
	}
}

func TestLoginEmailSetupStoresPendingEmailOnPhoneCodeHash(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	sender := &captureMailSender{}
	svc := NewService(memory.NewPasswordStore(),
		WithUsers(memory.NewUserStore()),
		WithLoginEmailVerification(codes, sender, time.Minute, 2, 6))
	if err := codes.Set(ctx, "phone-hash", store.PhoneCode{Phone: "15550010006", Channel: "email_setup_required", MaxAttempts: 2}, time.Minute); err != nil {
		t.Fatalf("seed phone code: %v", err)
	}

	if _, _, err := svc.SendLoginEmailCode(ctx, 0, "+1 555 001 0006", "phone-hash", "new@example.test", true); err != nil {
		t.Fatalf("SendLoginEmailCode setup: %v", err)
	}
	email, err := svc.VerifyLoginEmail(ctx, 0, "+1 555 001 0006", "phone-hash", sender.code, true)
	if err != nil {
		t.Fatalf("VerifyLoginEmail setup: %v", err)
	}
	if email != "new@example.test" {
		t.Fatalf("verified setup email = %q", email)
	}
	rec, found, err := codes.Get(ctx, "phone-hash")
	if err != nil || !found {
		t.Fatalf("phone code found=%v err=%v", found, err)
	}
	if rec.Channel != "email_login" || rec.Code != sender.code || rec.Email != "new@example.test" || !rec.VerifiedEmail || rec.PendingEmail != "new@example.test" {
		t.Fatalf("phone code after verify = %+v", rec)
	}
}

func TestVerifyLoginEmailDeletesCodeAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	sender := &captureMailSender{}
	svc := NewService(memory.NewPasswordStore(),
		WithUsers(users),
		WithLoginEmailVerification(codes, sender, time.Minute, 2, 6))
	u := createUser(t, users, "15550010007")

	if _, _, err := svc.SendLoginEmailCode(ctx, u.ID, "", "", "limit@example.test", false); err != nil {
		t.Fatalf("SendLoginEmailCode: %v", err)
	}
	bad1 := "000000"
	if bad1 == sender.code {
		bad1 = "111111"
	}
	bad2 := "222222"
	if bad2 == sender.code {
		bad2 = "333333"
	}
	if _, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", bad1, false); !errors.Is(err, domain.ErrEmailCodeInvalid) {
		t.Fatalf("first bad VerifyLoginEmail err = %v, want ErrEmailCodeInvalid", err)
	}
	if _, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", bad2, false); !errors.Is(err, domain.ErrEmailCodeInvalid) {
		t.Fatalf("second bad VerifyLoginEmail err = %v, want ErrEmailCodeInvalid", err)
	}
	if _, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", sender.code, false); !errors.Is(err, domain.ErrEmailCodeInvalid) {
		t.Fatalf("VerifyLoginEmail after max attempts err = %v, want ErrEmailCodeInvalid", err)
	}
	if _, found, _ := svc.LoginEmail(ctx, u.ID); found {
		t.Fatal("login email was set after exhausted verification code")
	}
}
