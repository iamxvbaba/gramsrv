package memory

import (
	"context"
	"slices"
	"testing"

	"telesrv/internal/domain"
)

// 回归：礼物实例行在所有权转移时被复用 —— id 保持不变、gift_date 被重置为
// 转移时刻。资料页必须按收到时刻倒序，否则被转送的礼物会停在旧 id 的位置，
// 而不是收礼人列表的顶部。
func TestStarGiftProfileOrdersByReceivedDate(t *testing.T) {
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	store := NewStarGiftStore()

	// id 更小的行拿到最新的收到时刻（转移后的实例行）。
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

	// 逐页行走：游标必须同时带日期，否则第二页按 id 续排会重复/漏项。
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

// 回归：TDesktop 的礼物盒「Мои коллекционные」页签用
// exclude_upgradable + exclude_unupgradable 取自己的收藏品。两个互斥条件若真的
// 互相抵消，结果恒为空，页签永远不出现（data_star_gift.cpp:
// MyUniqueGiftsSlice 里两个 flag 一起下发）。但放宽单个标志会破坏资料页的
// Upgradeable / Limited 分类过滤，所以只在两个同时出现时整体放弃过滤。
func TestStarGiftUpgradableFiltersDoNotCancelOut(t *testing.T) {
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1003}
	store := NewStarGiftStore()
	// 有库存的可升级礼物与已升级的收藏品。
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
	// 没有升级档的普通礼物：既不可升级，也不是收藏品。
	plain, err := store.Create(ctx, domain.SavedStarGift{
		Owner: owner, GiftID: 8002, RevisionID: 9002, MsgID: 302, Date: 1700000100,
	})
	if err != nil {
		t.Fatalf("create plain gift: %v", err)
	}

	// 桌面端自有礼物请求的原样组合：exclude_unlimited + 两个互斥标志同时下发。
	// 结果必须是自己的收藏品，而不是恒空的列表。
	page, err := store.ListByOwnerFiltered(ctx, domain.SavedStarGiftFilter{
		Owner: owner, ExcludeUnlimited: true, ExcludeUpgradable: true, ExcludeUnupgradable: true, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list own gifts: %v", err)
	}
	if len(page.Gifts) != 1 || page.Gifts[0].ID != collectible {
		t.Fatalf("own gifts = %+v, want exactly the collectible %d", page.Gifts, collectible)
	}

	// 单个标志必须保持原义：exclude_unupgradable 只留「可升级」，收藏品
	// （没有可升级性）不通过——资料页的 Limited 分类依赖这条。
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

	// exclude_upgradable 单独使用仍然只排除可升级的普通礼物。
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
