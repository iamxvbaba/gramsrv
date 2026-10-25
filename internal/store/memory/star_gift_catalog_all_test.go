package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestCatalogAllIncludesDisabledGiftForPackImport(t *testing.T) {
	ctx := context.Background()
	store := NewStarGiftStore()
	store.SeedCatalog([]domain.StarGift{{ID: 10, Title: "Old Pack Gift"}})
	if changed, err := store.SetCatalogEnabled(ctx, 10, false); err != nil || !changed {
		t.Fatalf("disable: changed=%v err=%v", changed, err)
	}
	visible, err := store.Catalog(ctx)
	if err != nil || len(visible) != 0 {
		t.Fatalf("visible catalog=%+v err=%v", visible, err)
	}
	all, err := store.CatalogAll(ctx)
	if err != nil || len(all) != 1 || all[0].Title != "Old Pack Gift" {
		t.Fatalf("full catalog=%+v err=%v", all, err)
	}
}
