package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type catalogRPCService struct {
	GiftsService
	gifts []domain.StarGift
	err   error
}

func (s *catalogRPCService) Catalog(context.Context) ([]domain.StarGift, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]domain.StarGift(nil), s.gifts...), nil
}

// The Bot API catalog must be the same enabled set the MTProto gift picker
// serves, otherwise a gift id a bot reads over HTTP could be one the checkout
// refuses.
func TestBotAPIAvailableGiftsServesTheEnabledCatalog(t *testing.T) {
	catalog := []domain.StarGift{
		{ID: 6011, Stars: 15, Limited: true, AvailabilityTotal: 1000, AvailabilityRemains: 734, SoldOut: false},
		{ID: 6012, Stars: 25, Limited: true, AvailabilityTotal: 500, AvailabilityRemains: 0, SoldOut: true},
		{ID: 6013, Stars: 50, LockedUntilDate: 1_900_000_000},
		{ID: 6014, Stars: 75, Auction: true, AuctionSlug: "post-2026"},
	}
	r := New(Config{DC: 2}, Deps{Gifts: &catalogRPCService{gifts: catalog}}, zaptest.NewLogger(t), clock.System)

	gifts, err := r.BotAPIAvailableGifts(context.Background())
	if err != nil {
		t.Fatalf("BotAPIAvailableGifts: %v", err)
	}
	if len(gifts) != len(catalog) {
		t.Fatalf("gifts len = %d, want %d", len(gifts), len(catalog))
	}
	for i, gift := range gifts {
		if gift.ID != catalog[i].ID || gift.Stars != catalog[i].Stars {
			t.Fatalf("gift[%d] = %+v, want %+v", i, gift, catalog[i])
		}
	}
}

func TestBotAPIAvailableGiftsWithoutCatalogFails(t *testing.T) {
	r := New(Config{DC: 2}, Deps{}, zaptest.NewLogger(t), clock.System)

	if _, err := r.BotAPIAvailableGifts(context.Background()); err == nil ||
		err.Error() != "STAR_GIFT_UNAVAILABLE" {
		t.Fatalf("err = %v, want STAR_GIFT_UNAVAILABLE", err)
	}
}

func TestBotAPIAvailableGiftsPropagatesCatalogFailure(t *testing.T) {
	want := errors.New("catalog read failed")
	r := New(Config{DC: 2}, Deps{Gifts: &catalogRPCService{err: want}}, zaptest.NewLogger(t), clock.System)

	if _, err := r.BotAPIAvailableGifts(context.Background()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}
