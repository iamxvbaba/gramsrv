package domain

import (
	"errors"
	"testing"
)

func TestStarGiftCatalogInventoryNormalization(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      int64
		remains int
		soldOut bool
		want    int
	}{
		{"new", 0, 0, false, 10},
		{"existing exhausted", 5, 0, false, 0},
		{"existing partial", 5, 4, false, 4},
		{"new sold out", 0, 0, true, 0},
		{"invalid negative is not repaired", 0, -1, false, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := StarGiftCatalogWrite{GiftID: tc.id, AvailabilityTotal: 10, AvailabilityRemains: tc.remains, SoldOut: tc.soldOut}
			w.NormalizeLifecycleAuthoring(1000)
			if !w.Limited || w.AvailabilityRemains != tc.want {
				t.Fatalf("normalized=%+v", w)
			}
		})
	}
}

func TestStarGiftCatalogInventoryReplacement(t *testing.T) {
	for _, remains := range []int{0, 4, 10} {
		current := StarGift{ID: 5, Limited: true, AvailabilityTotal: 10, AvailabilityRemains: remains,
			AvailabilityResale: 2, ResellMinStars: 30, FirstSaleDate: 100, LastSaleDate: 200}
		for _, omitted := range []bool{false, true} {
			w := StarGiftCatalogWrite{GiftID: 5, Limited: true, AvailabilityTotal: 10, AvailabilityRemains: 10}
			if omitted {
				w.Limited, w.AvailabilityTotal = false, 0
			}
			if err := w.PreserveCatalogInventory(current); err != nil {
				t.Fatal(err)
			}
			if !w.Limited || w.AvailabilityTotal != 10 || w.AvailabilityRemains != remains ||
				w.AvailabilityResale != 2 || w.ResellMinStars != 30 || w.FirstSaleDate != 100 || w.LastSaleDate != 200 {
				t.Fatalf("replacement discarded live inventory: %+v", w)
			}
		}
	}
	current := StarGift{ID: 5, Limited: true, AvailabilityTotal: 10, AvailabilityRemains: 4}
	for name, mutate := range map[string]func(*StarGiftCatalogWrite, *StarGift){
		"increase": func(w *StarGiftCatalogWrite, _ *StarGift) { w.AvailabilityTotal = 11 },
		"decrease": func(w *StarGiftCatalogWrite, _ *StarGift) { w.AvailabilityTotal = 9 },
		"negative": func(w *StarGiftCatalogWrite, _ *StarGift) { w.AvailabilityTotal = -1 },
		"unlimited to limited": func(_ *StarGiftCatalogWrite, c *StarGift) {
			c.Limited, c.AvailabilityTotal, c.AvailabilityRemains = false, 0, 0
		},
		"auction mode change":      func(w *StarGiftCatalogWrite, _ *StarGift) { w.Auction = true },
		"wrong identity":           func(w *StarGiftCatalogWrite, _ *StarGift) { w.GiftID = 6 },
		"negative persisted stock": func(_ *StarGiftCatalogWrite, c *StarGift) { c.AvailabilityRemains = -1 },
		"overfull persisted stock": func(_ *StarGiftCatalogWrite, c *StarGift) { c.AvailabilityRemains = 11 },
	} {
		t.Run(name, func(t *testing.T) {
			w := StarGiftCatalogWrite{GiftID: 5, Limited: true, AvailabilityTotal: 10}
			c := current
			mutate(&w, &c)
			if err := w.PreserveCatalogInventory(c); !errors.Is(err, ErrStarGiftLifecycleInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
