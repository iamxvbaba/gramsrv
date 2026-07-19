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

	// 首次授予：从 now 起算 3 个月。
	granted, err := svc.GrantPremium(ctx, u.ID, 3)
	if err != nil {
		t.Fatalf("GrantPremium: %v", err)
	}
	wantMin := time.Now().AddDate(0, 3, 0).Add(-time.Minute).Unix()
	wantMax := time.Now().AddDate(0, 3, 0).Add(time.Minute).Unix()
	if int64(granted.PremiumUntil) < wantMin || int64(granted.PremiumUntil) > wantMax {
		t.Fatalf("first grant until = %d, want ~now+3mo [%d,%d]", granted.PremiumUntil, wantMin, wantMax)
	}

	// 未过期续期：在现有到期时间上累加。
	renewed, err := svc.GrantPremium(ctx, u.ID, 1)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	base := time.Unix(int64(granted.PremiumUntil), 0)
	if got, want := int64(renewed.PremiumUntil), base.AddDate(0, 1, 0).Unix(); got != want {
		t.Fatalf("renew until = %d, want %d (accumulated)", got, want)
	}

	// 清除。
	cleared, err := svc.GrantPremium(ctx, u.ID, 0)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.PremiumUntil != 0 {
		t.Fatalf("cleared until = %d, want 0", cleared.PremiumUntil)
	}

	// 已过期重授：从 now 起算而非累加。
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

	// bot 拒绝授予。
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
		t.Fatalf("swept = %+v, want 仅过期用户且 until 清零", swept)
	}
	// 幂等：第二轮无事可做。
	again, err := svc.SweepExpiredPremium(ctx, now, 100)
	if err != nil || len(again) != 0 {
		t.Fatalf("second sweep = %+v err %v, want empty", again, err)
	}
	// 活跃用户不受影响。
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

	// 非会员设置被拒（PREMIUM_ACCOUNT_REQUIRED）。
	if _, err := svc.UpdateEmojiStatus(ctx, u.ID, domain.UserEmojiStatus{DocumentID: 42}); !errors.Is(err, domain.ErrPremiumRequired) {
		t.Fatalf("non-premium set err = %v, want ErrPremiumRequired", err)
	}

	// 会员可设置；到期清理后残值不再下发，但显式清除仍允许。
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
