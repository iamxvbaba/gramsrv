package domain

import "testing"

func TestNormalizeStarGiftCollectionDisplayItemsUsesVectorOrderWhenOrderIsZero(t *testing.T) {
	got, err := NormalizeStarGiftCollectionDisplayItems(
		[]int64{101, 202},
		[]StarGiftCollectionDisplayItem{
			{GiftID: 202, Order: 0, Size: StarGiftCollectionDisplaySizeExpanded},
			{GiftID: 101, Order: 0, Size: StarGiftCollectionDisplaySizeCompact},
		},
	)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(got) != 2 || got[0].GiftID != 202 || got[0].Order != 0 ||
		got[1].GiftID != 101 || got[1].Order != 1 {
		t.Fatalf("normalized order = %#v", got)
	}
}

func TestNormalizeStarGiftCollectionDisplayItemsRejectsIncompleteOrder(t *testing.T) {
	_, err := NormalizeStarGiftCollectionDisplayItems(
		[]int64{101, 202},
		[]StarGiftCollectionDisplayItem{{GiftID: 101, Order: 0, Size: StarGiftCollectionDisplaySizeCompact}},
	)
	if err != ErrStarGiftCollectionDisplayInvalid {
		t.Fatalf("error = %v, want invalid display settings", err)
	}
}
