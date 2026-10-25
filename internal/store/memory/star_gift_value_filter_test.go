package memory

import (
	"context"
	"slices"
	"testing"

	"telesrv/internal/domain"
)

func TestStarGiftFilteredListingOrdersPagesAndFiltersByValue(t *testing.T) {
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 2002}
	store := NewStarGiftStore()
	store.SeedCatalog([]domain.StarGift{
		{ID: 8101, Stars: 1000, PeerColorAvailable: false},
		{ID: 8102, Stars: 500, PeerColorAvailable: true},
	})

	ids := make([]int64, 4)
	for i := range ids {
		gift := domain.SavedStarGift{
			Owner: owner, GiftID: 8101, RevisionID: 9001, MsgID: 300 + i,
			Date: 1700000000 + i,
		}
		if i%2 == 1 {
			gift.GiftID = 8102
		}
		if i == 3 {
			gift.UniqueGiftID = 777
		}
		id, err := store.Create(ctx, gift)
		if err != nil {
			t.Fatalf("create gift %d: %v", i, err)
		}
		ids[i] = id
	}

	base := domain.SavedStarGiftFilter{Owner: owner, Limit: 10}
	all, err := store.ListByOwnerFiltered(ctx, base)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if all.Count != 4 || len(all.Gifts) != 4 {
		t.Fatalf("list all = count %d gifts %d, want 4", all.Count, len(all.Gifts))
	}

	byValue := base
	byValue.SortByValue = true
	valuePage, err := store.ListByOwnerFiltered(ctx, byValue)
	if err != nil {
		t.Fatalf("list by value: %v", err)
	}
	valueOrder := []int64{ids[2], ids[0], ids[3], ids[1]}
	if !slices.Equal(pageIDs(valuePage), valueOrder) {
		t.Fatalf("by value = %v, want %v", pageIDs(valuePage), valueOrder)
	}

	var paged []int64
	offset := ""
	for page := 0; ; page++ {
		pagedFilter := byValue
		pagedFilter.Limit = 1
		pagedFilter.Offset = offset
		part, err := store.ListByOwnerFiltered(ctx, pagedFilter)
		if err != nil {
			t.Fatalf("paged list %d: %v", page, err)
		}
		if part.Count != 4 || len(part.Gifts) == 0 {
			t.Fatalf("paged page %d = count %d gifts %d", page, part.Count, len(part.Gifts))
		}
		paged = append(paged, part.Gifts[0].ID)
		offset = part.NextOffset
		if offset == "" {
			break
		}
		if page > 8 {
			t.Fatalf("paging did not terminate, offset %q", offset)
		}
	}
	if !slices.Equal(paged, valueOrder) {
		t.Fatalf("paged by value = %v, want %v", paged, valueOrder)
	}

	if err := store.SetPinned(ctx, owner, []int64{ids[0], ids[2]}); err != nil {
		t.Fatalf("set pinned: %v", err)
	}
	profile, err := store.ListByOwnerFiltered(ctx, base)
	if err != nil {
		t.Fatalf("list profile: %v", err)
	}
	profileOrder := []int64{ids[0], ids[2], ids[3], ids[1]}
	if !slices.Equal(pageIDs(profile), profileOrder) {
		t.Fatalf("profile order = %v, want %v", pageIDs(profile), profileOrder)
	}

	colored := base
	colored.PeerColorAvailable = true
	coloredPage, err := store.ListByOwnerFiltered(ctx, colored)
	if err != nil {
		t.Fatalf("list peer color: %v", err)
	}
	if !slices.Equal(pageIDs(coloredPage), []int64{ids[3]}) {
		t.Fatalf("peer color page = %v, want [%d]", pageIDs(coloredPage), ids[3])
	}

	withoutHosted := base
	withoutHosted.ExcludeHosted = true
	hostedPage, err := store.ListByOwnerFiltered(ctx, withoutHosted)
	if err != nil {
		t.Fatalf("list excluding hosted: %v", err)
	}
	if !slices.Equal(pageIDs(hostedPage), pageIDs(profile)) {
		t.Fatalf("exclude_hosted page = %v, want %v (hosted rows are not produced in memory)",
			pageIDs(hostedPage), pageIDs(profile))
	}
}

func TestStarGiftValueSortUsesListingPriceOverValueAndCatalog(t *testing.T) {
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 2003}
	store := NewStarGiftStore()
	store.SeedCatalog([]domain.StarGift{
		{ID: 8201, Stars: 100, PeerColorAvailable: false},
		{ID: 8202, Stars: 200, PeerColorAvailable: false},
	})

	var ids []int64
	for i := 0; i < 2; i++ {
		gift := domain.SavedStarGift{
			Owner: owner, GiftID: 8201, RevisionID: int64(9100 + i), MsgID: 400 + i,
			Date: 1700000000 + i, UniqueGiftID: int64(500 + i),
		}
		id, err := store.Create(ctx, gift)
		if err != nil {
			t.Fatalf("create gift %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	store.SeedUnique(domain.UniqueStarGift{
		ID: 500, ValueCurrency: "XTR", ValueAmount: 300,
	})
	store.SeedUnique(domain.UniqueStarGift{
		ID: 501, ValueCurrency: "XTR", ValueAmount: 150,
	})

	store.SeedListings(500, []domain.StarGiftAmount{
		{Currency: domain.StarGiftCurrencyStars, Amount: 500},
	})
	store.SeedListings(501, []domain.StarGiftAmount{
		{Currency: domain.StarGiftCurrencyTON, Amount: 9999},
	})

	byValue := domain.SavedStarGiftFilter{Owner: owner, Limit: 10, SortByValue: true}
	page, err := store.ListByOwnerFiltered(ctx, byValue)
	if err != nil {
		t.Fatalf("list by value: %v", err)
	}

	want := []int64{ids[0], ids[1]}
	if !slices.Equal(pageIDs(page), want) {
		t.Fatalf("by value = %v, want %v (listing 500 > value 150 > catalog 100)", pageIDs(page), want)
	}
}

func pageIDs(page domain.SavedStarGiftPage) []int64 {
	out := make([]int64, 0, len(page.Gifts))
	for _, gift := range page.Gifts {
		out = append(out, gift.ID)
	}
	return out
}
