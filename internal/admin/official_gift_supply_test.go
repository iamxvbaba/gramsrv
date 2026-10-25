package admin

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"telesrv/internal/officialgifts"
)

func TestOfficialStarGiftSupplyIgnoresInactiveCollectibleFields(t *testing.T) {
	for _, sourceTotal := range []int{0, 10} {
		for _, supply := range []int{0, 1, 20} {
			t.Run(fmt.Sprintf("source-%d-request-%d", sourceTotal, supply), func(t *testing.T) {
				gifts := &fakeGiftsService{}
				source := &fakeOfficialGiftsSource{bundle: officialgifts.Bundle{
					Gift:         officialgifts.Gift{ID: 10, Stars: 50, AvailabilityTotal: sourceTotal},
					BaseDocument: officialgifts.Document{FileName: "gift.tgs", Data: []byte("gift"), SHA256: strings.Repeat("a", 64)},
				}}
				svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Gifts: gifts, OfficialGifts: source, Now: fixedNow})
				_, err := svc.ImportOfficialStarGift(context.Background(), ImportOfficialStarGiftRequest{
					CommandMeta:  CommandMeta{CommandID: "inactive-pool", Actor: "test", Reason: "supply"},
					SourceGiftID: "10", SupplyTotal: supply, IncludeCollectible: false, Enabled: true,
				})
				if err != nil {
					t.Fatal(err)
				}
				catalog := gifts.lastBundle.Catalog
				if catalog.Limited || catalog.AvailabilityTotal != 0 || catalog.AvailabilityRemains != 0 {
					t.Fatalf("inactive pool changed base supply: %+v", catalog)
				}
			})
		}
	}
}

func TestOfficialStarGiftAuctionStillHasFiniteSupplyWithoutCollectibles(t *testing.T) {
	gifts := &fakeGiftsService{}
	source := &fakeOfficialGiftsSource{bundle: officialgifts.Bundle{
		Gift:         officialgifts.Gift{ID: 10, Stars: 50, Auction: true, AuctionSlug: "spring", GiftsPerRound: 1, AvailabilityTotal: 3},
		BaseDocument: officialgifts.Document{FileName: "gift.tgs", Data: []byte("gift"), SHA256: strings.Repeat("a", 64)},
	}}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Gifts: gifts, OfficialGifts: source, Now: fixedNow})
	_, err := svc.ImportOfficialStarGift(context.Background(), ImportOfficialStarGiftRequest{
		CommandMeta: CommandMeta{CommandID: "auction-pool", Actor: "test", Reason: "supply"}, SourceGiftID: "10", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := gifts.lastBundle.Catalog
	if !w.Limited || !w.Auction || w.AvailabilityTotal != 3 || w.AvailabilityRemains != 3 || w.AuctionStartDate <= 0 {
		t.Fatalf("auction inventory=%+v", w)
	}
	if err := w.ValidateLifecycleAuthoring(int(fixedNow().Unix())); err != nil {
		t.Fatal(err)
	}
}
