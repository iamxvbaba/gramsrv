package account

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/store/memory"
)

func setConfirmedPassword(t *testing.T, svc *Service, sender *captureMailSender, userID int64, input domain.PasswordInputSettings) error {
	t.Helper()
	err := svc.UpdatePasswordSettings(context.Background(), userID, domain.PasswordCheck{Empty: true}, input)
	var pending *domain.EmailUnconfirmedError
	if !errors.As(err, &pending) || pending.CodeLength != 6 {
		t.Fatalf("expected confirmation: %v", err)
	}
	return svc.ConfirmPasswordEmail(context.Background(), userID, sender.code)
}

func passwordChangeFixture(t *testing.T) (*Service, *memory.PasswordStore, *captureMailSender, domain.PasswordInputSettings) {
	t.Helper()
	store := memory.NewPasswordStore()
	sender := &captureMailSender{}
	svc := NewService(store, WithLoginEmailVerification(memory.NewCodeStore(), sender, time.Minute, 3, 6))
	settings, err := svc.GetPassword(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	algo := settings.NewAlgo
	algo.Salt1 = append(algo.Salt1, make([]byte, 32)...)
	return svc, store, sender, domain.PasswordInputSettings{NewAlgo: &algo, NewPasswordHash: verifierForPassword(algo, []byte("password")), HasEmail: true, Email: "owner@example.test"}
}

func TestRecoveryEmailConfirmationResendCancelAndSingleWinner(t *testing.T) {
	ctx := context.Background()
	svc, store, sender, input := passwordChangeFixture(t)
	var pending *domain.EmailUnconfirmedError
	if err := svc.UpdatePasswordSettings(ctx, 1, domain.PasswordCheck{Empty: true}, input); !errors.As(err, &pending) {
		t.Fatal(err)
	}
	settings, _, _ := store.GetByUser(ctx, 1)
	if settings.HasPassword || settings.HasRecovery || settings.PendingChange == nil || settings.EmailUnconfirmedPattern == "" {
		t.Fatal("pending factor became effective before proof")
	}
	if sender.requests[0].Purpose != otpdelivery.PurposeRecoveryEmailConfirmation {
		t.Fatal("wrong OTP purpose")
	}
	oldCode := sender.code
	if err := svc.ResendPasswordEmail(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if sender.requests[0].DeliveryID == sender.requests[1].DeliveryID {
		t.Fatal("resend reused delivery")
	}
	if oldCode != sender.code {
		if err := svc.ConfirmPasswordEmail(ctx, 1, oldCode); !errors.Is(err, domain.ErrEmailCodeInvalid) {
			t.Fatal("old code survived resend", err)
		}
	}
	// A concurrent independent factor change must survive confirmation.
	if err := svc.SetLoginEmail(ctx, 1, "login@example.test"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); outcomes <- svc.ConfirmPasswordEmail(ctx, 1, sender.code) }()
	}
	wg.Wait()
	close(outcomes)
	winners := 0
	for err := range outcomes {
		if err == nil {
			winners++
		} else if !errors.Is(err, domain.ErrEmailCodeInvalid) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
	settings, _, _ = store.GetByUser(ctx, 1)
	if !settings.HasPassword || !settings.HasRecovery || settings.LoginEmail != "login@example.test" || settings.PendingChange != nil {
		t.Fatal("confirmed factor invariant")
	}
	challenge, err := svc.GetPassword(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	check := clientPasswordCheck(t, challenge, []byte("password"))
	if err := svc.UpdatePasswordSettings(ctx, 1, check, domain.PasswordInputSettings{HasEmail: true, Email: "replacement@example.test"}); !errors.As(err, &pending) {
		t.Fatal(err)
	}
	replacement := sender.code
	if err := svc.CancelPasswordEmail(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPasswordEmail(ctx, 1, replacement); !errors.Is(err, domain.ErrEmailCodeInvalid) {
		t.Fatal(err)
	}
	settings, _, _ = store.GetByUser(ctx, 1)
	if settings.RecoveryEmail != input.Email {
		t.Fatal("cancel changed verified address")
	}
	challenge, _ = svc.GetPassword(ctx, 1)
	if err := svc.UpdatePasswordSettings(ctx, 1, clientPasswordCheck(t, challenge, []byte("password")), domain.PasswordInputSettings{}); err != nil {
		t.Fatal(err)
	}
	settings, _, _ = store.GetByUser(ctx, 1)
	if settings.HasPassword || settings.HasRecovery || settings.LoginEmail != "login@example.test" {
		t.Fatal("clearing 2FA changed login factor")
	}
}

func TestRecoveryEmailConfirmationFailureExpiryAndAttemptBound(t *testing.T) {
	for _, scenario := range []string{"rejected", "unknown", "expired", "attempts"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			svc, store, sender, input := passwordChangeFixture(t)
			switch scenario {
			case "rejected":
				sender.err = errors.New("provider rejected")
			case "unknown":
				sender.err = &otpdelivery.OutcomeUnknownError{Cause: errors.New("timeout")}
			}
			err := svc.UpdatePasswordSettings(ctx, 1, domain.PasswordCheck{Empty: true}, input)
			settings, _, _ := store.GetByUser(ctx, 1)
			if scenario == "rejected" {
				if err == nil || settings.PendingChange != nil || settings.HasRecovery {
					t.Fatal("rejected delivery staged a usable proof")
				}
				return
			}
			var pending *domain.EmailUnconfirmedError
			if !errors.As(err, &pending) {
				t.Fatal(err)
			}
			if scenario == "expired" {
				if err := store.Mutate(ctx, 1, func(s *domain.PasswordSettings) error {
					s.PendingChange.ExpiresAt = time.Now().Add(-time.Second)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "attempts" {
				for i := 0; i < 3; i++ {
					if err := svc.ConfirmPasswordEmail(ctx, 1, "invalid"); !errors.Is(err, domain.ErrEmailCodeInvalid) {
						t.Fatal(err)
					}
				}
			}
			err = svc.ConfirmPasswordEmail(ctx, 1, sender.code)
			if scenario == "unknown" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, domain.ErrEmailCodeInvalid) {
				t.Fatal("expired/locked proof accepted", err)
			}
		})
	}
}
