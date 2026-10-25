package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestConfirmPhoneRejectsInvalidIdentityAfterPolicyChange(t *testing.T) {
	original := domain.PhoneFormatCheckEnabled()
	t.Cleanup(func() { domain.ConfigurePhoneFormatCheck(original) })
	for _, operation := range []string{"resend", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			domain.ConfigurePhoneFormatCheck(false)
			ctx := context.Background()
			users := memory.NewUserStore()
			u, err := users.Create(ctx, domain.User{Phone: "7123", FirstName: "Policy"})
			if err != nil {
				t.Fatal(err)
			}
			codes := memory.NewCodeStore()
			authKey := [8]byte{8}
			lifecycle := &fakeAccountLifecycleStore{snapshot: domain.AccountDeletionSnapshot{User: u}}
			svc := NewService(memory.NewPasswordStore(), WithUsers(users),
				WithPhoneChange(nil, nil, codes, nil, "12345", 5*time.Minute, 5),
				WithAccountLifecycle(lifecycle))
			token, digest, err := newAccountDeletionToken()
			if err != nil {
				t.Fatal(err)
			}
			lifecycle.pending = &domain.AccountDeletionRequest{
				ID: 1, UserID: u.ID, RequesterAuthKeyID: [8]byte{9},
				State: domain.AccountDeletionPending, ConfirmHashDigest: digest,
				ExecuteAt: time.Now().Add(accountDeletionDelay),
			}
			hash, _, err := svc.SendConfirmPhoneCode(ctx, u.ID, authKey, 77, token)
			if err != nil {
				t.Fatal(err)
			}
			domain.ConfigurePhoneFormatCheck(true)
			if operation == "resend" {
				_, _, handled, callErr := svc.ResendConfirmPhoneCode(ctx, u.ID, authKey, 77, "not-a-phone", hash)
				if !handled || !errors.Is(callErr, domain.ErrPhoneCodeInvalid) {
					t.Fatalf("resend invalid identity: handled=%v err=%v", handled, callErr)
				}
			} else {
				handled, callErr := svc.CancelConfirmPhoneCode(ctx, u.ID, authKey, "not-a-phone", hash)
				if !handled || !errors.Is(callErr, domain.ErrPhoneCodeInvalid) {
					t.Fatalf("cancel invalid identity: handled=%v err=%v", handled, callErr)
				}
			}
			if _, found, err := codes.Get(ctx, hash); err != nil || !found {
				t.Fatalf("invalid input consumed the original code: found=%v err=%v", found, err)
			}
		})
	}
}
