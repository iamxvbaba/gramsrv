package users

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestGrantPremiumSemantics(t *testing.T) {
	ctx := context.Background()
	store := memory.NewUserStore()
	u, err := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000101", FirstName: "P"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	svc := NewService(store)

	// First grant: starts from now for 3 months.
	granted, err := svc.GrantPremium(ctx, u.ID, 3)
	if err != nil {
		t.Fatalf("GrantPremium: %v", err)
	}
	wantMin := time.Now().AddDate(0, 3, 0).Add(-time.Minute).Unix()
	wantMax := time.Now().AddDate(0, 3, 0).Add(time.Minute).Unix()
	if int64(granted.PremiumUntil) < wantMin || int64(granted.PremiumUntil) > wantMax {
		t.Fatalf("first grant until = %d, want ~now+3mo [%d,%d]", granted.PremiumUntil, wantMin, wantMax)
	}

	// Renewal before expiry: accumulates onto the existing expiry time.
	renewed, err := svc.GrantPremium(ctx, u.ID, 1)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	base := time.Unix(int64(granted.PremiumUntil), 0)
	if got, want := int64(renewed.PremiumUntil), base.AddDate(0, 1, 0).Unix(); got != want {
		t.Fatalf("renew until = %d, want %d (accumulated)", got, want)
	}

	// Clear.
	cleared, err := svc.GrantPremium(ctx, u.ID, 0)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.PremiumUntil != 0 {
		t.Fatalf("cleared until = %d, want 0", cleared.PremiumUntil)
	}

	// Re-grant after expiry: starts from now rather than accumulating.
	if _, err := store.SetPremiumUntil(ctx, u.ID, int(time.Now().Add(-time.Hour).Unix())); err != nil {
		t.Fatalf("seed expired: %v", err)
	}
	regranted, err := svc.GrantPremium(ctx, u.ID, 3)
	if err != nil {
		t.Fatalf("regrant: %v", err)
	}
	if int64(regranted.PremiumUntil) < wantMin {
		t.Fatalf("regrant until = %d, want from now (≥%d)", regranted.PremiumUntil, wantMin)
	}

	// A bot is refused a grant.
	if _, err := svc.GrantPremium(ctx, domain.BotFatherUserID, 3); !errors.Is(err, domain.ErrPremiumBotUnsupported) {
		t.Fatalf("grant bot err = %v, want ErrPremiumBotUnsupported", err)
	}
}

func TestSweepExpiredPremium(t *testing.T) {
	ctx := context.Background()
	store := memory.NewUserStore()
	expired, _ := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000102", FirstName: "E"})
	active, _ := store.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000103", FirstName: "A"})
	now := time.Now().Unix()
	if _, err := store.SetPremiumUntil(ctx, expired.ID, int(now-10)); err != nil {
		t.Fatalf("seed expired: %v", err)
	}
	if _, err := store.SetPremiumUntil(ctx, active.ID, int(now+3600)); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	svc := NewService(store)

	swept, err := svc.SweepExpiredPremium(ctx, now, 100)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(swept) != 1 || swept[0].ID != expired.ID || swept[0].PremiumUntil != 0 {
		t.Fatalf("swept = %+v, want only expired users with until cleared", swept)
	}
	// Idempotent: the second round has nothing to do.
	again, err := svc.SweepExpiredPremium(ctx, now, 100)
	if err != nil || len(again) != 0 {
		t.Fatalf("second sweep = %+v err %v, want empty", again, err)
	}
	// Active users are unaffected.
	got, _, _ := store.ByID(ctx, active.ID)
	if got.PremiumUntil != int(now+3600) {
		t.Fatalf("active until = %d, want untouched", got.PremiumUntil)
	}
}

func TestUpdateEmojiStatusPremiumGate(t *testing.T) {
	ctx := context.Background()
	store := memory.NewUserStore()
	u, _ := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000104", FirstName: "S"})
	svc := NewService(store)

	// A non-Premium set is refused (PREMIUM_ACCOUNT_REQUIRED).
	if _, err := svc.UpdateEmojiStatus(ctx, u.ID, domain.UserEmojiStatus{DocumentID: 42}); !errors.Is(err, domain.ErrPremiumRequired) {
		t.Fatalf("non-premium set err = %v, want ErrPremiumRequired", err)
	}

	// A Premium user can set it; after expiry cleanup the leftover value is no longer delivered, but an explicit clear is still allowed.
	if _, err := store.SetPremiumUntil(ctx, u.ID, int(time.Now().Add(time.Hour).Unix())); err != nil {
		t.Fatalf("grant: %v", err)
	}
	set, err := svc.UpdateEmojiStatus(ctx, u.ID, domain.UserEmojiStatus{DocumentID: 42})
	if err != nil || set.EmojiStatusDocumentID != 42 {
		t.Fatalf("premium set = %+v err %v, want document 42", set, err)
	}
	if _, err := store.SetPremiumUntil(ctx, u.ID, 0); err != nil {
		t.Fatalf("downgrade: %v", err)
	}
	cleared, err := svc.UpdateEmojiStatus(ctx, u.ID, domain.UserEmojiStatus{})
	if err != nil || cleared.EmojiStatusDocumentID != 0 {
		t.Fatalf("clear after downgrade = %+v err %v, want cleared", cleared, err)
	}
}

func TestPremiumActiveUsesBaseUser(t *testing.T) {
	ctx := context.Background()
	store := memory.NewUserStore()
	u, _ := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000105", FirstName: "B"})
	svc := NewService(store)
	if svc.PremiumActive(ctx, u.ID) {
		t.Fatal("non-premium user reported active")
	}
	if _, err := store.SetPremiumUntil(ctx, u.ID, int(time.Now().Add(time.Hour).Unix())); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if !svc.PremiumActive(ctx, u.ID) {
		t.Fatal("premium user reported inactive")
	}
}
