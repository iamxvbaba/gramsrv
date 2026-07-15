package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestAccountGetPasswordUsesPendingPasswordUser(t *testing.T) {
	ctx := pendingPasswordContext()
	const userID int64 = 42

	passwords := memory.NewPasswordStore()
	if err := passwords.Save(ctx, userID, domain.PasswordSettings{
		HasPassword: true,
		Hint:        "q1",
		SRPID:       99,
		SRPVerifier: []byte{1},
	}); err != nil {
		t.Fatalf("save password: %v", err)
	}

	router := New(Config{}, Deps{
		Auth: &captureAuthService{
			pendingPasswordUserID: userID,
			pendingPassword:       true,
		},
		Account: appaccount.NewService(passwords),
	}, zaptest.NewLogger(t), clock.System)

	got, err := router.onAccountGetPassword(ctx)
	if err != nil {
		t.Fatalf("account.getPassword: %v", err)
	}
	if !got.HasPassword || got.Hint != "q1" || got.SRPID == 0 || len(got.SRPB) == 0 || got.CurrentAlgo == nil {
		t.Fatalf("password challenge = %+v, want pending user's SRP challenge", got)
	}
}

func TestAuthRecoverPasswordCompletesPendingSignIn(t *testing.T) {
	ctx := pendingPasswordContext()
	authKeyID, _ := AuthKeyIDFrom(ctx)
	const userID int64 = 42

	passwords := memory.NewPasswordStore()
	if err := passwords.Save(ctx, userID, domain.PasswordSettings{
		HasPassword:   true,
		Hint:          "q1",
		SRPID:         99,
		SRPVerifier:   []byte{1},
		RecoveryEmail: "alice@example.com",
	}); err != nil {
		t.Fatalf("save password: %v", err)
	}

	auth := &captureAuthService{
		pendingPasswordUserID: userID,
		pendingPassword:       true,
	}
	sessions := &captureSessions{}
	router := New(Config{}, Deps{
		Auth:     auth,
		Account:  appaccount.NewService(passwords),
		Users:    staticUsersService{user: domain.User{ID: userID, AccessHash: 7, Phone: "15550000042", FirstName: "Alice"}},
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	if _, err := router.onAuthRequestPasswordRecovery(ctx); err != nil {
		t.Fatalf("auth.requestPasswordRecovery: %v", err)
	}
	if _, err := router.onAuthRecoverPassword(ctx, &tg.AuthRecoverPasswordRequest{Code: "12345"}); err != nil {
		t.Fatalf("auth.recoverPassword: %v", err)
	}
	if auth.completePasswordCount != 1 || auth.completedPasswordKey != authKeyID {
		t.Fatalf("CompletePasswordSignIn count=%d key=%x, want one call for %x", auth.completePasswordCount, auth.completedPasswordKey, authKeyID)
	}
	if snap := sessions.snapshot(); snap.userID != userID || !snap.userResolved {
		t.Fatalf("session user = %d resolved=%v, want %d resolved", snap.userID, snap.userResolved, userID)
	}
	cleared, _, err := passwords.GetByUser(ctx, userID)
	if err != nil {
		t.Fatalf("get recovered password: %v", err)
	}
	if cleared.HasPassword {
		t.Fatalf("password still enabled after recover: %+v", cleared)
	}
}

func pendingPasswordContext() context.Context {
	authKeyID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	ctx := context.Background()
	ctx = WithAuthKeyID(ctx, authKeyID)
	ctx = WithRawAuthKeyID(ctx, authKeyID)
	ctx = WithSessionID(ctx, 77)
	return ctx
}
