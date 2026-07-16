package stargifts

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newTestService(gifts []domain.StarGift) (*Service, *memory.StarGiftStore) {
	st := memory.NewStarGiftStore()
	st.SeedCatalog(gifts)
	return NewService(st, nil, 2), st
}

func TestCatalogCachedAndHash(t *testing.T) {
	gifts := []domain.StarGift{
		{ID: 1, RevisionID: 11, Stars: 15, ConvertStars: 15, Title: "Heart"},
		{ID: 2, RevisionID: 12, Stars: 50, ConvertStars: 50, Title: "Cake"},
	}
	svc, _ := newTestService(gifts)
	ctx := context.Background()

	got, err := svc.Catalog(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("catalog = %d err %v, want 2", len(got), err)
	}
	// 再取一次命中进程内目录缓存。
	if _, err := svc.Catalog(ctx); err != nil {
		t.Fatalf("catalog#2: %v", err)
	}
	hash, err := svc.CatalogHash(ctx)
	if err != nil || hash != domain.StarGiftCatalogHash(gifts) {
		t.Fatalf("hash = %d err %v, want %d", hash, err, domain.StarGiftCatalogHash(gifts))
	}
	if g, ok, _ := svc.GiftByID(ctx, 2); !ok || g.Stars != 50 {
		t.Fatalf("GiftByID(2) = %+v ok %v, want Cake 50", g, ok)
	}
	if _, ok, _ := svc.GiftByID(ctx, 999); ok {
		t.Fatalf("GiftByID(999) found, want missing")
	}
}

func TestSavedGiftLifecycle(t *testing.T) {
	svc, _ := newTestService(nil)
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}

	id, err := svc.RecordSavedGift(ctx, domain.SavedStarGift{
		Owner: owner, FromUserID: 2002, GiftID: 1, RevisionID: 11, MsgID: 50, Date: 1700000000, ConvertStars: 15,
	})
	if err != nil || id == 0 {
		t.Fatalf("RecordSavedGift = %d err %v", id, err)
	}
	collection, err := svc.CreateCollection(ctx, owner, "Inbox", []int64{id})
	if err != nil || len(collection.GiftIDs) != 1 {
		t.Fatalf("CreateCollection = %+v err %v", collection, err)
	}

	page, err := svc.ListSaved(ctx, owner, false, "", 100)
	if err != nil || len(page.Gifts) != 1 || page.Count != 1 {
		t.Fatalf("list = %d count %d err %v, want 1/1", len(page.Gifts), page.Count, err)
	}
	if page.NextOffset != "" {
		t.Fatalf("single page next_offset = %q, want empty", page.NextOffset)
	}

	// 隐藏（unsave=true）→ excludeUnsaved 列表为空。
	ref := domain.SavedStarGiftRef{Owner: owner, MsgID: 50}
	if ok, err := svc.ToggleSaved(ctx, ref, true); err != nil || !ok {
		t.Fatalf("ToggleSaved hide = %v err %v", ok, err)
	}
	hidden, _ := svc.ListSaved(ctx, owner, true, "", 100)
	if len(hidden.Gifts) != 0 {
		t.Fatalf("excludeUnsaved list = %d, want 0 after hide", len(hidden.Gifts))
	}
	// 不带 exclude 仍能看到。
	all, _ := svc.ListSaved(ctx, owner, false, "", 100)
	if len(all.Gifts) != 1 {
		t.Fatalf("full list = %d, want 1 (hidden still listed)", len(all.Gifts))
	}

	// 转换回 Stars → 标记 converted，从列表消失。
	saved, err := svc.Convert(ctx, ref)
	if err != nil || saved.ConvertStars != 15 {
		t.Fatalf("Convert = %+v err %v, want ConvertStars 15", saved, err)
	}
	after, _ := svc.ListSaved(ctx, owner, false, "", 100)
	if len(after.Gifts) != 0 {
		t.Fatalf("list after convert = %d, want 0", len(after.Gifts))
	}
	collections, err := svc.ListCollections(ctx, owner)
	if err != nil || len(collections) != 1 || len(collections[0].GiftIDs) != 0 ||
		collections[0].Hash != domain.StarGiftCollectionHash("Inbox", nil) {
		t.Fatalf("collection after convert = %+v err %v, want empty membership and refreshed hash", collections, err)
	}
	// 重复转换被拒。
	if _, err := svc.Convert(ctx, ref); !errors.Is(err, domain.ErrStarGiftAlreadyConverted) {
		t.Fatalf("double convert err = %v, want ErrStarGiftAlreadyConverted", err)
	}
}

func TestChannelSavedGiftAllocatesSavedIDWithoutMessage(t *testing.T) {
	svc, _ := newTestService(nil)
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeChannel, ID: 2001}

	savedID, err := svc.RecordSavedGift(ctx, domain.SavedStarGift{
		Owner: owner, FromUserID: 1001, GiftID: 1, RevisionID: 11, MsgID: 0, SavedID: 0,
		Date: 1700000000, ConvertStars: 15,
	})
	if err != nil || savedID == 0 {
		t.Fatalf("RecordSavedGift(channel) = %d err %v, want allocated saved_id", savedID, err)
	}

	gift, found, err := svc.GetSaved(ctx, domain.SavedStarGiftRef{Owner: owner, SavedID: savedID})
	if err != nil || !found {
		t.Fatalf("GetSaved(channel) found=%v err=%v, want hit", found, err)
	}
	if gift.MsgID != 0 || gift.SavedID != savedID {
		t.Fatalf("channel saved gift ids = msg_id %d saved_id %d, want 0/%d", gift.MsgID, gift.SavedID, savedID)
	}
}

func TestSavedGiftPagination(t *testing.T) {
	svc, _ := newTestService(nil)
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	for i := 0; i < 5; i++ {
		if _, err := svc.RecordSavedGift(ctx, domain.SavedStarGift{
			Owner: owner, FromUserID: 2002, GiftID: 1, RevisionID: 11, MsgID: 100 + i, Date: 1700000000 + i, ConvertStars: 15,
		}); err != nil {
			t.Fatalf("record#%d: %v", i, err)
		}
	}
	page1, _ := svc.ListSaved(ctx, owner, false, "", 2)
	if len(page1.Gifts) != 2 || page1.NextOffset == "" {
		t.Fatalf("page1 = %d next=%q, want 2 + next", len(page1.Gifts), page1.NextOffset)
	}
	page2, _ := svc.ListSaved(ctx, owner, false, page1.NextOffset, 2)
	page3, _ := svc.ListSaved(ctx, owner, false, page2.NextOffset, 2)
	if len(page3.Gifts) != 1 || page3.NextOffset != "" {
		t.Fatalf("page3 = %d next=%q, want 1 + empty (terminal)", len(page3.Gifts), page3.NextOffset)
	}
}
