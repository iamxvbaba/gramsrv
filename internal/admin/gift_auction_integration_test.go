package admin

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strconv"
	"strings"
	"telesrv/internal/store/postgres"
	"testing"
	"time"
)

func TestImportedGiftAuctionBundlePostgres(t *testing.T) {
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(cfg.ConnConfig.Database), "test") {
		t.Fatal("dedicated test database required")
	}
	if err = postgres.Migrate(dsn); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	svc, gifts, source := auctionTemplateStore(t, postgres.NewStarGiftStore(pool))
	req := auctionRequest(source.Catalog.Gift)
	req.AuctionSlug = "imported-release-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	req.AuctionRoundDuration = 75
	preview, err := svc.CreateStarGiftAuction(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.SourceSHA = preview.Details["source_sha256"].(string)
	req.DryRun = false
	req.CommandID = "exec-postgres-release"
	result, err := svc.CreateStarGiftAuction(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(result.Details["gift_id"].(string), 10, 64)
	release, ok, err := postgres.NewStarGiftStore(pool).CatalogGift(ctx, id)
	if err != nil || !ok || release.AvailabilityTotal != 4 || release.AuctionRoundDuration != 75 || release.UpgradeTotal != 4 {
		t.Fatalf("release=%+v ok=%v err=%v", release, ok, err)
	}
	copied, ok, err := gifts.CollectiblePreview(ctx, id)
	if err != nil || !ok || len(copied.Models) != 2 || !copied.Models[1].Crafted || copied.UpgradeStars != 25 || copied.Backdrops[0].CenterColor != 0x123456 {
		t.Fatalf("copied=%+v ok=%v err=%v", copied, ok, err)
	}
	original, ok, err := gifts.CollectiblePreview(ctx, source.Catalog.Gift.ID)
	if err != nil || !ok || original.SupplyTotal != 8 {
		t.Fatal("source pool changed")
	}
	// A separate command cannot claim the same auction/collectible namespace.
	req.CommandID = "dry-conflicting-release"
	req.DryRun = true
	if _, err = svc.CreateStarGiftAuction(ctx, req); err == nil {
		t.Fatal("duplicate release slug accepted")
	}
}
