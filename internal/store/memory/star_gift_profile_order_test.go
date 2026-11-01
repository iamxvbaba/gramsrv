package memory

import (
	"context"
	"slices"
	"testing"

	"telesrv/internal/domain"
)

// Regression: the gift instance row is reused when ownership is transferred — the id stays
// the same and gift_date is reset to the transfer time. The profile page must order by received
// time descending, otherwise a transferred gift stays at its old id's position instead of the
// top of the recipient's list.
func TestStarGiftProfileOrdersByReceivedDate(t *testing.T) {
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	store := NewStarGiftStore()

	// The row with the smaller id gets the newest received time (the post-transfer instance row).
	transferred, err := store.Create(ctx, domain.SavedStarGift{
		Owner: owner, GiftID: 8001, RevisionID: 9001, MsgID: 200, Date: 1700000900,
	})
	if err != nil {
		t.Fatalf("create transferred: %v", err)
	}
	regular, err := store.Create(ctx, domain.SavedStarGift{
		Owner: owner, GiftID: 8001, RevisionID: 9001, MsgID: 201, Date: 1700000100,
	})
	if err != nil {
		t.Fatalf("create regular: %v", err)
	}

	page, err := store.ListByOwner(ctx, owner, false, "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []int64{transferred, regular}
	var got []int64
	for _, gift := range page.Gifts {
		got = append(got, gift.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("date order = %v, want %v (newest received first)", got, want)
	}

	// Walk page by page: the cursor must carry the date too, otherwise paging by id on the second page duplicates or skips entries.
	got = got[:0]
	offset := ""
	for pageNumber := 0; ; pageNumber++ {
		paged, err := store.ListByOwner(ctx, owner, false, offset, 1)
		if err != nil {
			t.Fatalf("list page %d: %v", pageNumber, err)
		}
		if len(paged.Gifts) != 1 {
			t.Fatalf("page %d = %d gifts, want 1", pageNumber, len(paged.Gifts))
		}
		got = append(got, paged.Gifts[0].ID)
		if paged.NextOffset == "" {
			break
		}
		offset = paged.NextOffset
	}
	if !slices.Equal(got, want) {
		t.Fatalf("paged date order = %v, want %v", got, want)
	}
}

// Regression: TDesktop's gift box "My collectibles" tab uses
// exclude_upgradable + exclude_unupgradable to fetch one's own collectibles. If the two mutually
// exclusive conditions truly cancelled out, the result would always be empty and the tab would
// never appear (in data_star_gift.cpp: MyUniqueGiftsSlice the two flags are sent together). But
// relaxing a single flag would break the profile page's Upgradeable / Limited category filtering,
// so filtering is dropped entirely only when both appear together.
func TestStarGiftUpgradableFiltersDoNotCancelOut(t *testing.T) {
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1003}
	store := NewStarGiftStore()
	// An upgradable gift with stock and an already-upgraded collectible.
	store.SeedCatalog([]domain.StarGift{
		{ID: 8001, RevisionID: 9001, Title: "Upgradable", UpgradeStars: 100, UpgradeIssued: 0, UpgradeTotal: 10},
	})

	upgradable, err := store.Create(ctx, domain.SavedStarGift{
		Owner: owner, GiftID: 8001, RevisionID: 9001, MsgID: 300, Date: 1700000300,
	})
	if err != nil {
		t.Fatalf("create upgradable gift: %v", err)
	}
	collectible, err := store.Create(ctx, domain.SavedStarGift{
		Owner: owner, GiftID: 8001, RevisionID: 9001, MsgID: 301, Date: 1700000200, UniqueGiftID: 777,
	})
	if err != nil {
		t.Fatalf("create collectible: %v", err)
	}
	// A plain gift with no upgrade tier: neither upgradable nor a collectible.
	plain, err := store.Create(ctx, domain.SavedStarGift{
		Owner: owner, GiftID: 8002, RevisionID: 9002, MsgID: 302, Date: 1700000100,
	})
	if err != nil {
		t.Fatalf("create plain gift: %v", err)
	}

	// The desktop client's own-gifts request exactly as sent: exclude_unlimited plus both mutually
	// exclusive flags. The result must be one's own collectibles, not an always-empty list.
	page, err := store.ListByOwnerFiltered(ctx, domain.SavedStarGiftFilter{
		Owner: owner, ExcludeUnlimited: true, ExcludeUpgradable: true, ExcludeUnupgradable: true, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list own gifts: %v", err)
	}
	if len(page.Gifts) != 1 || page.Gifts[0].ID != collectible {
		t.Fatalf("own gifts = %+v, want exactly the collectible %d", page.Gifts, collectible)
	}

	// A single flag must keep its original meaning: exclude_unupgradable keeps only "upgradable",
	// so a collectible (which has no upgradability) does not pass — the profile page's Limited category relies on this.
	page, err = store.ListByOwnerFiltered(ctx, domain.SavedStarGiftFilter{
		Owner: owner, ExcludeUnupgradable: true, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list upgradable: %v", err)
	}
	ids := make([]int64, 0, len(page.Gifts))
	for _, gift := range page.Gifts {
		ids = append(ids, gift.ID)
	}
	if !slices.Equal(ids, []int64{upgradable}) {
		t.Fatalf("exclude_unupgradable = %v, want only [%d]", ids, upgradable)
	}

	// Used alone, exclude_upgradable still only excludes upgradable plain gifts.
	page, err = store.ListByOwnerFiltered(ctx, domain.SavedStarGiftFilter{
		Owner: owner, ExcludeUpgradable: true, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list not upgradable: %v", err)
	}
	ids = ids[:0]
	for _, gift := range page.Gifts {
		ids = append(ids, gift.ID)
	}
	if !slices.Equal(ids, []int64{collectible, plain}) {
		t.Fatalf("exclude_upgradable = %v, want [%d %d]", ids, collectible, plain)
	}
}

func TestStarGiftProfilePinOrderAndPagination(t *testing.T) {
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	store := NewStarGiftStore()
	ids := make([]int64, 4)
	for i := range ids {
		id, err := store.Create(ctx, domain.SavedStarGift{
			Owner: owner, GiftID: 8001, RevisionID: 9001, MsgID: 100 + i, Date: 1700000000 + i,
		})
		if err != nil {
			t.Fatalf("create gift %d: %v", i, err)
		}
		ids[i] = id
	}

	if err := store.SetPinned(ctx, owner, []int64{ids[0], ids[2]}); err != nil {
		t.Fatalf("set pinned: %v", err)
	}

	want := []int64{ids[0], ids[2], ids[3], ids[1]}
	var got []int64
	offset := ""
	for pageNumber := 0; ; pageNumber++ {
		page, err := store.ListByOwner(ctx, owner, false, offset, 1)
		if err != nil {
			t.Fatalf("list page %d: %v", pageNumber, err)
		}
		if page.Count != len(ids) || len(page.Gifts) != 1 {
			t.Fatalf("page %d = %+v, want count=%d and one gift", pageNumber, page, len(ids))
		}
		got = append(got, page.Gifts[0].ID)
		if page.NextOffset == "" {
			break
		}
		offset = page.NextOffset
	}
	if !slices.Equal(got, want) {
		t.Fatalf("paged order = %v, want %v", got, want)
	}
	if ok, err := store.SetUnsaved(ctx, domain.SavedStarGiftRef{Owner: owner, MsgID: 100}, true); err != nil || !ok {
		t.Fatalf("hide pinned gift = %v err %v", ok, err)
	}
	hidden, found, err := store.GetByRef(ctx, domain.SavedStarGiftRef{Owner: owner, MsgID: 100})
	if err != nil || !found || !hidden.Unsaved || hidden.PinnedOrder != 0 {
		t.Fatalf("hidden pinned gift = %+v found %v err %v", hidden, found, err)
	}
	remaining, found, err := store.GetByRef(ctx, domain.SavedStarGiftRef{Owner: owner, MsgID: 102})
	if err != nil || !found || remaining.PinnedOrder != 1 {
		t.Fatalf("remaining pin = %+v found %v err %v", remaining, found, err)
	}
	if err := store.SetPinned(ctx, owner, []int64{ids[0], ids[2]}); err != nil {
		t.Fatalf("repin hidden gift: %v", err)
	}
	repinned, found, err := store.GetByRef(ctx, domain.SavedStarGiftRef{Owner: owner, MsgID: 100})
	if err != nil || !found || repinned.Unsaved || repinned.PinnedOrder != 1 {
		t.Fatalf("repinned gift = %+v found %v err %v", repinned, found, err)
	}

	if err := store.SetPinned(ctx, owner, nil); err != nil {
		t.Fatalf("clear pinned: %v", err)
	}
	page, err := store.ListByOwner(ctx, owner, false, "", 10)
	if err != nil {
		t.Fatalf("list after clear: %v", err)
	}
	want = []int64{ids[3], ids[2], ids[1], ids[0]}
	got = got[:0]
	for _, gift := range page.Gifts {
		got = append(got, gift.ID)
		if gift.PinnedOrder != 0 {
			t.Fatalf("gift %d pinned_order=%d after clear", gift.ID, gift.PinnedOrder)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("order after clear = %v, want %v", got, want)
	}
}
