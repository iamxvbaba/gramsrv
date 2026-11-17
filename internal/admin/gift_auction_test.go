package admin

import (
	"context"
	"strconv"
	"strings"
	stargiftapp "telesrv/internal/app/stargifts"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
	"testing"
)

func auctionTemplate(t *testing.T) (*Service, *stargiftapp.Service, domain.StarGiftCatalogBundleResult) {
	return auctionTemplateStore(t, memory.NewStarGiftStore())
}
func auctionTemplateStore(t *testing.T, backend store.StarGiftStore) (*Service, *stargiftapp.Service, domain.StarGiftCatalogBundleResult) {
	t.Helper()
	gifts := stargiftapp.NewService(backend, &adminGiftBlob{data: map[string][]byte{}}, 2)
	animation, err := gifts.PrepareAnimation("template.json", []byte(`{"v":"5.7.4","fr":30,"ip":0,"op":60,"w":512,"h":512,"layers":[{"ty":4}],"assets":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	attr := func(kind domain.StarGiftCollectibleAttributeKind, name string) domain.StarGiftCollectibleAttribute {
		return domain.StarGiftCollectibleAttribute{Kind: kind, Name: name, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500, Animation: &animation}
	}
	crafted := attr(domain.StarGiftCollectibleModel, "Crafted")
	crafted.RarityKind = domain.StarGiftRarityEpic
	crafted.RarityPermille = 0
	crafted.Crafted = true
	source, err := gifts.CreateCatalogBundle(context.Background(), domain.StarGiftCatalogBundleWrite{
		Catalog: domain.StarGiftCatalogWrite{Title: "Template", Stars: 50, ConvertStars: 25, Enabled: false, Limited: true, AvailabilityTotal: 8, AvailabilityRemains: 8, Animation: animation, Actor: "ops", CommandID: "source", Birthday: true},
		Collectible: &domain.StarGiftCollectibleWrite{UpgradeStars: 25, SupplyTotal: 8, SlugPrefix: "template", CommandID: "source", Actor: "ops",
			Models:    []domain.StarGiftCollectibleAttribute{attr(domain.StarGiftCollectibleModel, "Model"), crafted},
			Patterns:  []domain.StarGiftCollectibleAttribute{attr(domain.StarGiftCollectiblePattern, "Pattern")},
			Backdrops: []domain.StarGiftCollectibleAttribute{{Kind: domain.StarGiftCollectibleBackdrop, Name: "Blue", BackdropID: 1, CenterColor: 0x123456, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500}, {Kind: domain.StarGiftCollectibleBackdrop, Name: "Red", BackdropID: 2, CenterColor: 0xff0000, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewService(Dependencies{Gifts: gifts, Commands: newMemoryCommandRepo(), Now: fixedNow}), gifts, source
}
func auctionRequest(source domain.StarGift) CreateStarGiftAuctionRequest {
	return CreateStarGiftAuctionRequest{
		CommandMeta: CommandMeta{CommandID: "dry-auction", Actor: "ops", Reason: "new release", DryRun: true}, SourceGiftID: source.ID, SourceRevisionID: source.RevisionID,
		Stars: 100, ConvertStars: 25, Enabled: true, AuctionSlug: "release", AvailabilityTotal: 4, GiftsPerRound: 2, AuctionRoundDuration: 3600,
	}
}
func TestAuctionFromImportedPreservesSourceAndCompleteUpgradePool(t *testing.T) {
	svc, gifts, source := auctionTemplate(t)
	ctx := context.Background()
	req := auctionRequest(source.Catalog.Gift)
	preview, err := svc.CreateStarGiftAuction(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := gifts.CatalogAll(ctx)
	if len(all) != 1 {
		t.Fatalf("dry run wrote a gift: %d", len(all))
	}
	req.SourceSHA = preview.Details["source_sha256"].(string)
	req.CommandID = "exec-auction"
	req.DryRun = false
	result, err := svc.CreateStarGiftAuction(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(result.Details["gift_id"].(string), 10, 64)
	if id == source.Catalog.Gift.ID {
		t.Fatal("source identity was reused")
	}
	all, _ = gifts.CatalogAll(ctx)
	if len(all) != 2 {
		t.Fatalf("catalog size=%d", len(all))
	}
	original, _, _ := gifts.CollectiblePreview(ctx, source.Catalog.Gift.ID)
	if original.SupplyTotal != 8 || original.SlugPrefix != "template" {
		t.Fatalf("source pool changed: %+v", original)
	}
	enabled, _ := gifts.Catalog(ctx)
	if len(enabled) != 1 || enabled[0].ID != id || !enabled[0].Auction || enabled[0].AvailabilityRemains != 4 || !enabled[0].Birthday || enabled[0].AuctionRoundDuration != 3600 {
		t.Fatalf("new catalog=%+v", enabled)
	}
	pool, ok, err := gifts.CollectiblePreview(ctx, id)
	if err != nil || !ok || pool.UpgradeStars != 25 || pool.SupplyTotal != 4 || pool.SlugPrefix != "release" || len(pool.Models) != 2 || !pool.Models[1].Crafted || pool.Models[1].RarityKind != domain.StarGiftRarityEpic || pool.Backdrops[0].CenterColor != 0x123456 {
		t.Fatalf("copied pool=%+v ok=%v err=%v", pool, ok, err)
	}
	raw, found, err := gifts.CollectibleAnimationJSON(ctx, id, domain.StarGiftCollectibleModel, pool.Models[1].ID)
	if err != nil || !found || len(raw) == 0 {
		t.Fatalf("copied animation missing: %v", err)
	}
	again, err := svc.CreateStarGiftAuction(ctx, req)
	if err != nil || !again.AlreadyExecuted {
		t.Fatalf("retry=%+v err=%v", again, err)
	}
	all, _ = gifts.CatalogAll(ctx)
	if len(all) != 2 {
		t.Fatal("retry duplicated the release")
	}
}
func TestAuctionFromImportedRejectsStalePreviewAndInvalidSlug(t *testing.T) {
	svc, gifts, source := auctionTemplate(t)
	ctx := context.Background()
	req := auctionRequest(source.Catalog.Gift)
	preview, err := svc.CreateStarGiftAuction(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.DryRun = false
	req.CommandID = "exec-stale"
	req.SourceSHA = preview.Details["source_sha256"].(string)
	req.Stars++
	if _, err = svc.CreateStarGiftAuction(ctx, req); err == nil || !strings.Contains(err.Error(), "SOURCE_GIFT_CHANGED") {
		t.Fatalf("stale preview err=%v", err)
	}
	req = auctionRequest(source.Catalog.Gift)
	req.SourceRevisionID++
	if _, err = svc.CreateStarGiftAuction(ctx, req); err == nil {
		t.Fatal("stale revision accepted")
	}
	req = auctionRequest(source.Catalog.Gift)
	req.AuctionSlug = "../../bad"
	if _, err = svc.CreateStarGiftAuction(ctx, req); err == nil {
		t.Fatal("invalid slug accepted")
	}
	all, _ := gifts.CatalogAll(ctx)
	if len(all) != 1 {
		t.Fatal("invalid requests wrote gifts")
	}
}

func TestAuctionFromImportedReleaseSettings(t *testing.T) {
	svc, gifts, source := auctionTemplate(t)
	ctx := context.Background()
	req := auctionRequest(source.Catalog.Gift)
	// Empty author explicitly clears it; absent author would preserve the template.
	author := ""
	premium, birthday := true, false
	limit := 2
	upgrade := int64(40)
	req.ReleasedBy = &author
	req.RequirePremium = &premium
	req.Birthday = &birthday
	req.PerUserTotal = &limit
	req.UpgradeStars = &upgrade
	req.SortOrder = 4
	preview, err := svc.CreateStarGiftAuction(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.SourceSHA = preview.Details["source_sha256"].(string)
	req.CommandID = "exec-settings"
	req.DryRun = false
	result, err := svc.CreateStarGiftAuction(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(result.Details["gift_id"].(string), 10, 64)
	all, _ := gifts.Catalog(ctx)
	if len(all) != 1 || !all[0].RequirePremium || all[0].Birthday || !all[0].LimitedPerUser || all[0].PerUserTotal != 2 || all[0].ReleasedBy.ID != 0 {
		t.Fatalf("release fields=%+v", all)
	}
	pool, _, _ := gifts.CollectiblePreview(ctx, id)
	if pool.UpgradeStars != 40 || pool.SupplyTotal != 4 {
		t.Fatalf("release pool=%+v", pool)
	}
	original, _, _ := gifts.CollectiblePreview(ctx, source.Catalog.Gift.ID)
	if original.UpgradeStars != 25 || original.SupplyTotal != 8 {
		t.Fatal("source settings changed")
	}
}
