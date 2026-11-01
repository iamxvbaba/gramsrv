package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	storepkg "telesrv/internal/store"
)

// TestStickerCollectionsRoundTripPostgres regression for migration 0006: personal sticker/GIF collections are persisted
// (newest first / truncation / unsave / clear / kind isolation). The store keeps only document_id (no FK).
func TestStickerCollectionsRoundTripPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewPasswordStore(pool)

	const owner = int64(778899001)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM edge_delivery_outbox_lanes WHERE stream_id = $1", owner)
		_, _ = pool.Exec(ctx, "DELETE FROM edge_delivery_outbox WHERE target_user_id = $1", owner)
		_, _ = pool.Exec(ctx, "DELETE FROM user_sticker_collections WHERE owner_user_id = $1", owner)
	})
	_, _ = pool.Exec(ctx, "DELETE FROM user_sticker_collections WHERE owner_user_id = $1", owner)

	faved := domain.StickerCollectionFaved
	// fave 101 then 102 -> newest first.
	if err := mutateStickerCollectionPG(ctx, store, domain.StickerCollectionMutation{OwnerUserID: owner, Kind: faved, Mutation: domain.StickerCollectionMutationSave, DocumentID: 101, Date: 1000}); err != nil {
		t.Fatalf("fave 101: %v", err)
	}
	if err := mutateStickerCollectionPG(ctx, store, domain.StickerCollectionMutation{OwnerUserID: owner, Kind: faved, Mutation: domain.StickerCollectionMutationSave, DocumentID: 102, Date: 1001}); err != nil {
		t.Fatalf("fave 102: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 2 || ids[0] != 102 || ids[1] != 101 {
		t.Fatalf("faved = %v, want [102 101]", ids)
	}

	// re-fave 101 (updates used_at) -> moves to front.
	if err := mutateStickerCollectionPG(ctx, store, domain.StickerCollectionMutation{OwnerUserID: owner, Kind: faved, Mutation: domain.StickerCollectionMutationSave, DocumentID: 101, Date: 1002}); err != nil {
		t.Fatalf("re-fave 101: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 2 || ids[0] != 101 {
		t.Fatalf("faved after re-fave = %v, want 101 first", ids)
	}

	// used_at has only second precision: same-second saves must still strictly order by the last mutation.
	if err := mutateStickerCollectionPG(ctx, store, domain.StickerCollectionMutation{OwnerUserID: owner, Kind: faved, Mutation: domain.StickerCollectionMutationSave, DocumentID: 102, Date: 3000}); err != nil {
		t.Fatalf("same-second fave 102: %v", err)
	}
	if err := mutateStickerCollectionPG(ctx, store, domain.StickerCollectionMutation{OwnerUserID: owner, Kind: faved, Mutation: domain.StickerCollectionMutationSave, DocumentID: 101, Date: 3000}); err != nil {
		t.Fatalf("same-second re-fave 101: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 2 || ids[0] != 101 || ids[1] != 102 {
		t.Fatalf("same-second order = %v, want [101 102]", ids)
	}

	// Capacity is a domain invariant; callers cannot override it. Exercise the
	// smaller recent-attached cap without a test-only max parameter.
	for i := 1; i <= domain.MaxRecentStickers+1; i++ {
		if err := mutateStickerCollectionPG(ctx, store, domain.StickerCollectionMutation{
			OwnerUserID: owner, Kind: domain.StickerCollectionRecentAttached,
			Mutation: domain.StickerCollectionMutationSave, DocumentID: int64(10_000 + i), Date: 1003 + i,
		}); err != nil {
			t.Fatalf("save recent-attached %d: %v", i, err)
		}
	}
	if ids := stickerIDs(t, store, ctx, owner, domain.StickerCollectionRecentAttached); len(ids) != domain.MaxRecentStickers || ids[0] != 10_000+domain.MaxRecentStickers+1 {
		t.Fatalf("recent-attached after trim = %v", ids)
	}

	// Kind isolation: recent and faved are independent.
	if err := mutateStickerCollectionPG(ctx, store, domain.StickerCollectionMutation{OwnerUserID: owner, Kind: domain.StickerCollectionRecent, Mutation: domain.StickerCollectionMutationSave, DocumentID: 999, Date: 2000}); err != nil {
		t.Fatalf("save recent: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, domain.StickerCollectionRecent); len(ids) != 1 || ids[0] != 999 {
		t.Fatalf("recent = %v, want [999]", ids)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 2 {
		t.Fatalf("faved polluted by recent: %v", ids)
	}

	// unsave + clear。
	if err := mutateStickerCollectionPG(ctx, store, domain.StickerCollectionMutation{OwnerUserID: owner, Kind: faved, Mutation: domain.StickerCollectionMutationUnsave, DocumentID: 101}); err != nil {
		t.Fatalf("unsave 101: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 1 || ids[0] != 102 {
		t.Fatalf("faved after unsave = %v, want [102]", ids)
	}
	if err := mutateStickerCollectionPG(ctx, store, domain.StickerCollectionMutation{OwnerUserID: owner, Kind: faved, Mutation: domain.StickerCollectionMutationClear}); err != nil {
		t.Fatalf("clear faved: %v", err)
	}
	if ids := stickerIDs(t, store, ctx, owner, faved); len(ids) != 0 {
		t.Fatalf("faved after clear = %v, want empty", ids)
	}
	// clear only clears that kind; recent remains.
	if ids := stickerIDs(t, store, ctx, owner, domain.StickerCollectionRecent); len(ids) != 1 {
		t.Fatalf("recent gone after clearing faved: %v", ids)
	}
}

func mutateStickerCollectionPG(ctx context.Context, passwordStore *PasswordStore, mutation domain.StickerCollectionMutation) error {
	return passwordStore.MutateStickerCollection(ctx, mutation, func(domain.StickerCollectionMutation) ([]storepkg.DeliveryEffect, error) {
		return []storepkg.DeliveryEffect{storepkg.AbsoluteDeliveryEffect(storepkg.DeliveryOutboxEnqueue{
			TargetUserID: mutation.OwnerUserID, Payload: []byte{1}, RecoveryPolicy: storepkg.OutboxRecoveryAbsoluteReload,
		})}, nil
	})
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
