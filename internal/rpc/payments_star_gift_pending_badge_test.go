package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

type pendingBadgeGifts struct {
	GiftsService
	pending map[int64]domain.StarGiftCollectibleAvailability
}

func (g pendingBadgeGifts) PendingCollectibleAvailability(context.Context, []int64) (map[int64]domain.StarGiftCollectibleAvailability, error) {
	return g.pending, nil
}

func TestScheduledCollectiblePoolShowsGiftAsLimited(t *testing.T) {
	r, sender, _, gift := starGiftTestRouter(t)
	ctx := WithUserID(context.Background(), sender.ID)
	plain := r.deps.Gifts
	r.deps.Gifts = pendingBadgeGifts{GiftsService: plain, pending: map[int64]domain.StarGiftCollectibleAvailability{
		gift.ID: {UpgradeStars: 100, SupplyTotal: 5000},
	}}

	res, err := r.onPaymentsGetStarGifts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := res.(*tg.PaymentsStarGifts).Gifts[0].(*tg.StarGift)
	total, _ := g.GetAvailabilityTotal()
	remains, _ := g.GetAvailabilityRemains()
	if !g.Limited || total != 5000 || remains != 5000 {
		t.Fatalf("scheduled gift = limited:%v total:%d remains:%d, want limited 5000/5000", g.Limited, total, remains)
	}

	// The badge is a projection: the shared catalog is untouched, so once the
	// pool opens the gift is plain again.
	r.deps.Gifts = plain
	res, err = r.onPaymentsGetStarGifts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if g := res.(*tg.PaymentsStarGifts).Gifts[0].(*tg.StarGift); g.Limited {
		t.Fatal("limited badge leaked into the shared catalog")
	}
}
