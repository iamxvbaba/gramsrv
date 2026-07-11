package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestStickerCollectionsRoundTripPostgres 回归迁移 0006：个人贴纸/GIF 集合持久化
// （最新置顶 / 截断 / unsave / clear / 类别隔离）。store 仅存 document_id（无 FK）。
func TestStickerCollectionsRoundTripPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewPasswordStore(pool)

	const owner = int64(778899001)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM user_sticker_collections WHERE owner_user_id = $1", owner) })
	_, _ = pool.Exec(ctx, "DELETE FROM user_sticker_collections WHERE owner_user_id = $1", owner)

	faved := domain.StickerCollectionFaved
	// fave 101 then 102 → 最新在前。
	if err := store.SaveStickerCollectionItem(ctx, owner, faved, 101, false, 1000, 100); err != nil {
		t.Fatalf("fave 101: %v", err)
	}
	if err := store.SaveStickerCollectionItem(ctx, owner, faved, 102, false, 1001, 100); err != nil {
		t.Fatalf("fave 102: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 2 || ids[0] != 102 || ids[1] != 101 {
		t.Fatalf("faved = %v, want [102 101]", ids)
	}

	// 重 fave 101（更新 used_at）→ 移到最前。
	if err := store.SaveStickerCollectionItem(ctx, owner, faved, 101, false, 1002, 100); err != nil {
		t.Fatalf("re-fave 101: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 2 || ids[0] != 101 {
		t.Fatalf("faved after re-fave = %v, want 101 first", ids)
	}

	// used_at 只有秒精度：同秒保存仍必须严格按最后一次 mutation 排序。
	if err := store.SaveStickerCollectionItem(ctx, owner, faved, 102, false, 3000, 100); err != nil {
		t.Fatalf("same-second fave 102: %v", err)
	}
	if err := store.SaveStickerCollectionItem(ctx, owner, faved, 101, false, 3000, 100); err != nil {
		t.Fatalf("same-second re-fave 101: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 2 || ids[0] != 101 || ids[1] != 102 {
		t.Fatalf("same-second order = %v, want [101 102]", ids)
	}

	// 截断：max=2，加第 3 个挤掉最旧。
	if err := store.SaveStickerCollectionItem(ctx, owner, faved, 103, false, 1003, 2); err != nil {
		t.Fatalf("fave 103 (max 2): %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 2 || ids[0] != 103 || ids[1] != 101 {
		t.Fatalf("faved after trim = %v, want [103 101]", ids)
	}

	// 类别隔离：recent 与 faved 独立。
	if err := store.SaveStickerCollectionItem(ctx, owner, domain.StickerCollectionRecent, 999, false, 2000, 30); err != nil {
		t.Fatalf("save recent: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, domain.StickerCollectionRecent); len(ids) != 1 || ids[0] != 999 {
		t.Fatalf("recent = %v, want [999]", ids)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 2 {
		t.Fatalf("faved polluted by recent: %v", ids)
	}

	// unsave + clear。
	if err := store.SaveStickerCollectionItem(ctx, owner, faved, 101, true, 0, 100); err != nil {
		t.Fatalf("unsave 101: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 1 || ids[0] != 103 {
		t.Fatalf("faved after unsave = %v, want [103]", ids)
	}
	if err := store.ClearStickerCollection(ctx, owner, faved); err != nil {
		t.Fatalf("clear faved: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 0 {
		t.Fatalf("faved after clear = %v, want empty", ids)
	}
	// clear 只清该类别，recent 仍在。
	if ids := stickerIDs(t, store, ctx, owner, domain.StickerCollectionRecent); len(ids) != 1 {
		t.Fatalf("recent gone after clearing faved: %v", ids)
	}
}

func stickerIDs(t *testing.T, store *PasswordStore, ctx context.Context, owner int64, kind domain.StickerCollectionKind) []int64 {
	t.Helper()
	items, err := store.ListStickerCollection(ctx, owner, kind, 100)
	if err != nil {
		t.Fatalf("list %s: %v", kind, err)
	}
	ids := make([]int64, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.DocumentID)
	}
	return ids
}
