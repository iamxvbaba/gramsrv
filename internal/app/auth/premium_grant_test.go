package auth

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestSignUpPremiumGrant 验证新注册账号默认赠送 3 个月会员（WithPremiumGrant），
// 以及 0 = 关闭赠送分支。
func TestSignUpPremiumGrant(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345", WithPremiumGrant(3))

	hash, err := svc.SendCode(ctx, "+15550004401")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	verifyCodeForSignUp(t, svc, "+15550004401", hash, "12345")
	u, _, err := svc.SignUp(ctx, domain.Authorization{}, "+15550004401", hash, "Prem", "User")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	wantMin := time.Now().AddDate(0, 3, 0).Add(-time.Minute).Unix()
	wantMax := time.Now().AddDate(0, 3, 0).Add(time.Minute).Unix()
	if int64(u.PremiumUntil) < wantMin || int64(u.PremiumUntil) > wantMax {
		t.Fatalf("PremiumUntil = %d, want ~now+3mo [%d,%d]", u.PremiumUntil, wantMin, wantMax)
	}
	if !u.PremiumActiveAt(time.Now().Unix()) {
		t.Fatal("new user should be premium active")
	}
}

func TestSignUpPremiumGrantDisabled(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345", WithPremiumGrant(0))

	hash, err := svc.SendCode(ctx, "+15550004402")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	verifyCodeForSignUp(t, svc, "+15550004402", hash, "12345")
	u, _, err := svc.SignUp(ctx, domain.Authorization{}, "+15550004402", hash, "Free", "User")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if u.PremiumUntil != 0 {
		t.Fatalf("PremiumUntil = %d, want 0 (grant disabled)", u.PremiumUntil)
	}
}
