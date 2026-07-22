package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestOfficialStarGiftBundleIsAtomicPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	store := NewStarGiftStore(pool)
	baseID := time.Now().UnixNano() & 0x7ffffffffffff000
	manifestSHA := make([]byte, 32)
	for i := range manifestSHA {
		manifestSHA[i] = 0x5a
	}
	attribute := func(kind domain.StarGiftCollectibleAttributeKind, id int64, name string) domain.StarGiftCollectibleAttribute {
		value := domain.StarGiftCollectibleAttribute{Kind: kind, Name: name, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 918}
		if kind == domain.StarGiftCollectibleBackdrop {
			value.BackdropID = int(id)
			value.CenterColor, value.EdgeColor, value.PatternColor, value.TextColor = 1, 2, 3, 4
			return value
		}
		if kind == domain.StarGiftCollectiblePattern {
			value.Document = collectibleTestPatternDocumentPtr(id, name+".tgs")
		} else {
			value.Document = collectibleTestDocumentPtr(id, name+".tgs")
		}
		value.Blob = collectibleTestBlobPtr(id, name)
		value.Animation = collectibleTestAnimationPtr(name + ".tgs")
		value.OfficialDocumentID = 5200000000000000000 + id%1000
		return value
	}
	bundle := domain.StarGiftCatalogBundleWrite{
		Catalog: domain.StarGiftCatalogWrite{
			Title: "Official", Stars: 50, ConvertStars: 25, Enabled: true,
			Document: collectibleTestDocument(baseID, "official.tgs"), Blob: collectibleTestBlob(baseID, "official"),
			Animation: collectibleTestAnimation("official.tgs"), Actor: "integration", CommandID: "official-catalog-" + suffix,
			OfficialGiftID: 5170145012310081615, SourceManifestSHA256: manifestSHA,
			OfficialSourceJSON: []byte(`{"id":5170145012310081615,"sold_out":true,"birthday":false}`),
		},
		Collectible: &domain.StarGiftCollectibleWrite{
			UpgradeStars: 100, SupplyTotal: 10, SlugPrefix: "official-" + suffix,
			Models: []domain.StarGiftCollectibleAttribute{
				attribute(domain.StarGiftCollectibleModel, baseID+1, "model"),
				attribute(domain.StarGiftCollectibleModel, baseID+3, "model-two"),
			},
			Patterns: []domain.StarGiftCollectibleAttribute{
				attribute(domain.StarGiftCollectiblePattern, baseID+2, "pattern"),
				attribute(domain.StarGiftCollectiblePattern, baseID+4, "pattern-two"),
			},
			Backdrops: []domain.StarGiftCollectibleAttribute{
				attribute(domain.StarGiftCollectibleBackdrop, 0, "backdrop"),
				attribute(domain.StarGiftCollectibleBackdrop, 1, "backdrop-two"),
			},
			Actor: "integration", CommandID: "official-pool-" + suffix,
			OfficialGiftID: 5170145012310081615, SourceManifestSHA256: manifestSHA,
		},
	}
	result, err := store.CreateCatalogBundle(ctx, bundle)
	if err != nil {
		t.Fatalf("create official bundle: %v", err)
	}
	if result.Catalog.Gift.ID == 0 || result.Collectible == nil || result.Catalog.Gift.UpgradeStars != 100 {
		t.Fatalf("bundle result = %+v", result)
	}
	var sourceID int64
	var soldOut bool
	if err := pool.QueryRow(ctx, `
SELECT official_gift_id, (official_source->>'sold_out')::boolean
FROM star_gift_catalog_revisions WHERE id=$1`, result.Catalog.Gift.RevisionID).Scan(&sourceID, &soldOut); err != nil {
		t.Fatal(err)
	}
	if sourceID != 5170145012310081615 || !soldOut {
		t.Fatalf("source id=%d sold_out=%v", sourceID, soldOut)
	}

	failing := bundle
	failing.Catalog.CommandID = "official-rollback-" + suffix
	failing.Catalog.Document = collectibleTestDocument(baseID+100, "rollback.tgs")
	failing.Catalog.Blob = collectibleTestBlob(baseID+100, "rollback")
	failing.Collectible = &domain.StarGiftCollectibleWrite{
		UpgradeStars: 100, SupplyTotal: 10, SlugPrefix: "rollback-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			attribute(domain.StarGiftCollectibleModel, baseID+101, "duplicate"),
			attribute(domain.StarGiftCollectibleModel, baseID+102, "duplicate"),
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			attribute(domain.StarGiftCollectiblePattern, baseID+103, "pattern"),
			attribute(domain.StarGiftCollectiblePattern, baseID+104, "pattern-two"),
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			attribute(domain.StarGiftCollectibleBackdrop, 0, "backdrop"),
			attribute(domain.StarGiftCollectibleBackdrop, 1, "backdrop-two"),
		},
		Actor: "integration", CommandID: "rollback-pool-" + suffix,
	}
	if _, err := store.CreateCatalogBundle(ctx, failing); !errors.Is(err, domain.ErrStarGiftCollectibleInvalid) {
		t.Fatalf("failing bundle err=%v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM star_gift_catalog_revisions WHERE command_id=$1`, failing.Catalog.CommandID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("failed bundle left %d catalog revisions", rows)
	}
}
