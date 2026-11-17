package postgres

import (
	"context"
	"telesrv/internal/domain"
	"testing"
	"time"
)

func TestStarGiftAuctionDurationSurvivesCatalogReloadPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	gifts := NewStarGiftStore(pool)
	id := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Duration " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 4, AvailabilityRemains: 4, GiftsPerRound: 2, AuctionStartDate: int(time.Now().Unix()), AuctionRoundDuration: 75, AuctionSlug: "duration-" + suffix,
		Document: collectibleTestDocument(id, "duration.tgs"), Blob: collectibleTestBlob(id, "duration"), Animation: collectibleTestAnimation("duration.tgs"), Actor: "integration", CommandID: "duration-" + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Gift.AuctionRoundDuration != 75 {
		t.Fatalf("created duration=%d", entry.Gift.AuctionRoundDuration)
	}
	loaded, ok, err := NewStarGiftStore(pool).CatalogGift(ctx, entry.Gift.ID)
	if err != nil || !ok || loaded.AuctionRoundDuration != 75 {
		t.Fatalf("reload duration=%d ok=%v err=%v", loaded.AuctionRoundDuration, ok, err)
	}
	historical, ok, err := gifts.CatalogRevision(ctx, entry.Gift.RevisionID)
	if err != nil || !ok || historical.AuctionRoundDuration != 75 {
		t.Fatalf("historical duration=%d ok=%v err=%v", historical.AuctionRoundDuration, ok, err)
	}
}
