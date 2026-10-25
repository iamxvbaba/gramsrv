package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestStarGiftCollectionSecondPagePostgres pins keyset paging inside a gift
// collection. Collection pages are ordered by id only, so the cursor binds only
// the id: binding the unused date left a parameter PostgreSQL cannot type
// ("could not determine data type of parameter"), and every second page failed.
func TestStarGiftCollectionSecondPagePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner := createTestUser(t, ctx, NewUserStore(pool), "+1886"+suffix+"01", "PageOwner", "")
	revisionID, giftID := ratingTestCatalogRevision(t, pool)
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	ids := make([]int64, 3)
	for i := range ids {
		if err := pool.QueryRow(ctx, `
INSERT INTO peer_star_gifts (owner_peer_type, owner_peer_id, msg_id, gift_id, gift_date, catalog_revision_id, lifecycle_status, converted)
VALUES ('user', $1, $2, $3, $4, $5, 'active', false) RETURNING id`, owner.ID, i+1, giftID, 1000+i, revisionID).Scan(&ids[i]); err != nil {
			t.Fatalf("seed saved gift: %v", err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM star_gift_collection_items WHERE saved_gift_id = ANY($1::bigint[])`, ids)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM star_gift_collections WHERE owner_peer_type='user' AND owner_peer_id=$1`, owner.ID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM peer_star_gifts WHERE id = ANY($1::bigint[])`, ids)
	})

	gifts := NewStarGiftStore(pool)
	collection, err := gifts.CreateCollection(ctx, ownerPeer, "Pages", ids)
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	filter := domain.SavedStarGiftFilter{Owner: ownerPeer, CollectionID: collection.CollectionID, Limit: 2}
	first, err := gifts.ListByOwnerFiltered(ctx, filter)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Gifts) != 2 || first.NextOffset == "" || first.Count != 3 {
		t.Fatalf("first page = %d gifts next=%q count=%d, want 2 of 3 with a cursor", len(first.Gifts), first.NextOffset, first.Count)
	}
	filter.Offset = first.NextOffset
	second, err := gifts.ListByOwnerFiltered(ctx, filter)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Gifts) != 1 || second.NextOffset != "" || second.Count != 3 {
		t.Fatalf("second page = %d gifts next=%q count=%d, want the last gift", len(second.Gifts), second.NextOffset, second.Count)
	}
	seen := make(map[int64]bool, len(ids))
	for _, g := range append(first.Gifts, second.Gifts...) {
		if seen[g.ID] {
			t.Fatalf("gift %d returned on both pages", g.ID)
		}
		seen[g.ID] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("gift %d missing from the collection pages", id)
		}
	}
}
