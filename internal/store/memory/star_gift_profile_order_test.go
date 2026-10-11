package memory

import (
	"context"
	"slices"
	"testing"

	"telesrv/internal/domain"
)

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
