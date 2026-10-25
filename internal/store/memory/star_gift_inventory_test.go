package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestStarGiftCatalogInventoryReplacementMemory(t *testing.T) {
	ctx := context.Background()
	s := NewStarGiftStore()
	first, err := s.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Limited: true, AvailabilityTotal: 10, AvailabilityRemains: 3, FirstSaleDate: 100, LastSaleDate: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{GiftID: first.Gift.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Gift.Limited || second.Gift.AvailabilityTotal != 10 || second.Gift.AvailabilityRemains != 3 ||
		second.Gift.FirstSaleDate != 100 || second.Gift.LastSaleDate != 200 {
		t.Fatalf("inventory=%+v", second.Gift)
	}
	_, err = s.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{GiftID: first.Gift.ID, Limited: true, AvailabilityTotal: 11})
	if !errors.Is(err, domain.ErrStarGiftLifecycleInvalid) {
		t.Fatalf("changed cap err=%v", err)
	}
	if got := s.catalog[first.Gift.ID]; got.RevisionID != second.Gift.RevisionID {
		t.Fatal("rejected cap change published a revision")
	}
}

func TestStarGiftCatalogSupportOnlyPersistsInMemory(t *testing.T) {
	ctx := context.Background()
	s := NewStarGiftStore()
	created, err := s.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{SupportOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	readBack, ok, err := s.CatalogRevision(ctx, created.Gift.RevisionID)
	if err != nil || !ok {
		t.Fatalf("CatalogRevision ok=%v err=%v", ok, err)
	}
	if !readBack.SupportOnly {
		t.Fatalf("read back SupportOnly=%v, want true", readBack.SupportOnly)
	}
}
