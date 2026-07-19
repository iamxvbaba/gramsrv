package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type collectibleEmojiGiftService struct {
	GiftsService
	gifts map[int64]domain.UniqueStarGift
}

func (s *collectibleEmojiGiftService) UniqueByID(_ context.Context, id int64) (domain.UniqueStarGift, bool, error) {
	gift, ok := s.gifts[id]
	return gift, ok, nil
}

func (s *collectibleEmojiGiftService) ListUniqueByOwner(_ context.Context, owner domain.Peer, limit int) ([]domain.UniqueStarGift, error) {
	out := make([]domain.UniqueStarGift, 0, len(s.gifts))
	for _, gift := range s.gifts {
		if gift.Owner == owner && !gift.Burned && gift.OwnerAddress == "" {
			out = append(out, gift)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func collectibleEmojiTestGift(ownerID int64) domain.UniqueStarGift {
	return domain.UniqueStarGift{
		ID: 9001, Title: "Plush Pepe", Slug: "PlushPepe-1",
		Owner:   domain.Peer{Type: domain.PeerTypeUser, ID: ownerID},
		Model:   domain.StarGiftCollectibleAttribute{Document: &domain.Document{ID: 7101}},
		Pattern: domain.StarGiftCollectibleAttribute{Document: &domain.Document{ID: 7201}},
		Backdrop: domain.StarGiftCollectibleAttribute{
			CenterColor: 0x102030, EdgeColor: 0x405060,
			PatternColor: 0x708090, TextColor: 0xa0b0c0,
		},
	}
}

func TestAccountCollectibleEmojiStatusListSetAndRejectNonOwner(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 1, Phone: "15550009101", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := userStore.Create(ctx, domain.User{AccessHash: 2, Phone: "15550009102", FirstName: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	users := appusers.NewService(userStore)
	if _, err := users.GrantPremium(ctx, owner.ID, 1); err != nil {
		t.Fatalf("grant premium: %v", err)
	}
	gift := collectibleEmojiTestGift(owner.ID)
	gifts := &collectibleEmojiGiftService{gifts: map[int64]domain.UniqueStarGift{gift.ID: gift}}
	r := New(Config{}, Deps{Users: users, Gifts: gifts}, zaptest.NewLogger(t), clock.System)
	ownerCtx := WithUserID(ctx, owner.ID)

	listed, err := r.onAccountGetCollectibleEmojiStatuses(ownerCtx, 0)
	if err != nil {
		t.Fatalf("get collectible statuses: %v", err)
	}
	statuses, ok := listed.(*tg.AccountEmojiStatuses)
	if !ok || len(statuses.Statuses) != 1 || statuses.Hash == 0 {
		t.Fatalf("collectible list = %T %#v", listed, listed)
	}
	collectible, ok := statuses.Statuses[0].(*tg.EmojiStatusCollectible)
	if !ok || collectible.CollectibleID != gift.ID || collectible.DocumentID != gift.Model.Document.ID ||
		collectible.PatternDocumentID != gift.Pattern.Document.ID || collectible.PatternColor != gift.Backdrop.PatternColor {
		t.Fatalf("collectible status = %T %#v", statuses.Statuses[0], statuses.Statuses[0])
	}
	if cached, err := r.onAccountGetCollectibleEmojiStatuses(ownerCtx, statuses.Hash); err != nil {
		t.Fatalf("get cached collectible statuses: %v", err)
	} else if _, ok := cached.(*tg.AccountEmojiStatusesNotModified); !ok {
		t.Fatalf("cached collectible statuses = %T, want notModified", cached)
	}

	input := &tg.InputEmojiStatusCollectible{CollectibleID: gift.ID}
	input.SetUntil(2_000_000_000)
	if ok, err := r.onAccountUpdateEmojiStatus(ownerCtx, input); err != nil || !ok {
		t.Fatalf("set collectible status: ok=%v err=%v", ok, err)
	}
	self, err := users.Self(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !self.EmojiStatusCollectible.Valid() || self.EmojiStatusCollectible.CollectibleID != gift.ID ||
		self.EmojiStatusUntil != 2_000_000_000 {
		t.Fatalf("persisted collectible status = %+v", self.EmojiStatus())
	}
	wire, ok := tgUserEmojiStatus(self, time.Now().Unix()).(*tg.EmojiStatusCollectible)
	if !ok || wire.Slug != gift.Slug || wire.TextColor != gift.Backdrop.TextColor {
		t.Fatalf("wire collectible = %T %#v", tgUserEmojiStatus(self, time.Now().Unix()), wire)
	}

	stolen := gift
	stolen.Owner = domain.Peer{Type: domain.PeerTypeUser, ID: other.ID}
	gifts.gifts[gift.ID] = stolen
	if ok, err := r.onAccountUpdateEmojiStatus(ownerCtx, &tg.InputEmojiStatusCollectible{CollectibleID: gift.ID}); ok || !tgerr.Is(err, "COLLECTIBLE_INVALID") {
		t.Fatalf("set non-owned collectible: ok=%v err=%v", ok, err)
	}
}

func TestCollectibleEmojiStatusDurableUpdateProjection(t *testing.T) {
	collectible, ok := domain.CollectibleEmojiStatus(collectibleEmojiTestGift(1))
	if !ok {
		t.Fatal("test gift should project")
	}
	value := domain.UserEmojiStatus{DocumentID: collectible.DocumentID, Collectible: collectible}
	update, ok := tgOtherUpdateFromEvent(domain.UpdateEvent{
		UserID: 1, Type: domain.UpdateEventUserEmojiStatus, EmojiStatus: value,
	}).(*tg.UpdateUserEmojiStatus)
	if !ok {
		t.Fatal("durable event did not produce updateUserEmojiStatus")
	}
	if status, ok := update.EmojiStatus.(*tg.EmojiStatusCollectible); !ok || status.PatternDocumentID != collectible.PatternDocumentID {
		t.Fatalf("durable wire status = %T %#v", update.EmojiStatus, update.EmojiStatus)
	}
}
