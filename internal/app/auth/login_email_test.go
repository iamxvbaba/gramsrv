package auth

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestSignInWithEmailCompletesLogin 验证旧客户端把 phone channel 放进
// email_verification 时仍可登录，但验证码必须精确匹配，不能用任意非空值绕过。
func TestSignInWithEmailCompletesLogin(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	svc := NewService(users, authz, memory.NewCodeStore(), nil, nil, "12345",
		WithLoginCodeDelivery(memory.NewLoginCodeDeliveryStore(messages, memory.NewUpdateEventStore())),
	)
	var key [8]byte
	key[0] = 0x42

	hash, err := svc.SendCode(ctx, "+15550009001")
	if err != nil {
		t.Fatalf("SendCode signup: %v", err)
	}
	verifyCodeForSignUp(t, svc, "+15550009001", hash, "12345")
	u, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: key}, "+15550009001", hash, "Email", "Login")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if err := svc.LogOut(ctx, key); err != nil {
		t.Fatalf("LogOut: %v", err)
	}

	hash, err = svc.SendCode(ctx, "+15550009001")
	if err != nil {
		t.Fatalf("SendCode signin: %v", err)
	}
	if _, _, _, err := svc.SignInWithEmail(ctx, domain.Authorization{AuthKeyID: key}, "+15550009001", hash, "anything-goes"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("SignInWithEmail arbitrary nonempty code err=%v, want ErrCodeInvalid", err)
	}
	got, _, needSignUp, err := svc.SignInWithEmail(ctx, domain.Authorization{AuthKeyID: key}, "+15550009001", hash, "12345")
	if err != nil {
		t.Fatalf("SignInWithEmail: %v", err)
	}
	if needSignUp || got.ID != u.ID {
		t.Fatalf("SignInWithEmail user=%+v needSignUp=%v, want existing user %d", got, needSignUp, u.ID)
	}
	bound, found, err := svc.UserID(ctx, key)
	if err != nil || !found || bound != u.ID {
		t.Fatalf("UserID after email signin = %d found=%v err=%v, want %d", bound, found, err, u.ID)
	}
}

// TestSignInWithEmailRejectsEmptyCode 空邮箱验证码必须被拒。
func TestSignInWithEmailRejectsEmptyCode(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345")
	hash, err := svc.SendCode(ctx, "+15550009002")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if _, _, _, err := svc.SignInWithEmail(ctx, domain.Authorization{}, "+15550009002", hash, "   "); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("SignInWithEmail empty code err = %v, want ErrCodeInvalid", err)
	}
}

// TestSignInWithEmailStillHonorsTwoFactor 登录邮箱与 2FA 正交：即使走邮箱验证码，
// 开启了两步验证的账号仍停在 SESSION_PASSWORD_NEEDED，不能绕过密码。
func TestSignInWithEmailStillHonorsTwoFactor(t *testing.T) {
	ctx := context.Background()
	passwords := memory.NewPasswordStore()
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		WithPasswords(passwords),
		WithLoginCodeDelivery(memory.NewLoginCodeDeliveryStore(messages, memory.NewUpdateEventStore())),
	)
	var key [8]byte
	key[0] = 0x43

	hash, err := svc.SendCode(ctx, "+15550009003")
	if err != nil {
		t.Fatalf("SendCode signup: %v", err)
	}
	verifyCodeForSignUp(t, svc, "+15550009003", hash, "12345")
	u, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: key}, "+15550009003", hash, "Two", "Factor")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if err := svc.LogOut(ctx, key); err != nil {
		t.Fatalf("LogOut: %v", err)
	}
	if err := passwords.Save(ctx, u.ID, domain.PasswordSettings{HasPassword: true}); err != nil {
		t.Fatalf("save password settings: %v", err)
	}

	hash, err = svc.SendCode(ctx, "+15550009003")
	if err != nil {
		t.Fatalf("SendCode signin: %v", err)
	}
	got, _, _, err := svc.SignInWithEmail(ctx, domain.Authorization{AuthKeyID: key}, "+15550009003", hash, "12345")
	if !errors.Is(err, domain.ErrSessionPasswordNeeded) {
		t.Fatalf("SignInWithEmail err = %v, want ErrSessionPasswordNeeded", err)
	}
	if got.ID != u.ID {
		t.Fatalf("SignInWithEmail user = %+v, want pending 2FA user %d", got, u.ID)
	}
	if bound, found, err := svc.UserID(ctx, key); err != nil || found || bound != 0 {
		t.Fatalf("UserID after email signin with 2FA = %d found=%v err=%v, want not-found", bound, found, err)
	}
}
