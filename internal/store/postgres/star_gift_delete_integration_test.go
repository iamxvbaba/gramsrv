package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestStarGiftDeleteSavedPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner, err := NewUserStore(pool).Create(ctx, domain.User{AccessHash: 81, Phone: "+1996" + suffix + "01", FirstName: "GiftDeleter"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	revisionID, giftID := ratingTestCatalogRevision(t, pool)
	if _, err := pool.Exec(ctx, `
INSERT INTO peer_star_gifts (owner_peer_type, owner_peer_id, msg_id, gift_id, gift_date, catalog_revision_id, lifecycle_status, converted, pinned_order)
VALUES ('user', $1, 1, $2, 0, $3, 'active', false, 1),
       ('user', $1, 2, $2, 0, $3, 'active', false, 2),
       ('user', $1, 3, $2, 0, $3, 'converted', true, 0)`, owner.ID, giftID, revisionID); err != nil {
		t.Fatalf("seed gifts: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM peer_star_gifts WHERE owner_peer_type='user' AND owner_peer_id=$1`, owner.ID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", owner.ID)
	})
	s := NewStarGiftStore(pool)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	ref := func(msgID int) domain.SavedStarGiftRef { return domain.SavedStarGiftRef{Owner: peer, MsgID: msgID} }

	if err := s.DeleteSaved(ctx, ref(1)); err != nil {
		t.Fatalf("delete pinned gift: %v", err)
	}
	if _, found, err := s.GetByRef(ctx, ref(1)); err != nil || found {
		t.Fatalf("deleted gift resolves: %v, %v", found, err)
	}
	var status string
	var pinned int
	if err := pool.QueryRow(ctx, `SELECT lifecycle_status,pinned_order FROM peer_star_gifts WHERE owner_peer_id=$1 AND msg_id=1`, owner.ID).Scan(&status, &pinned); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || pinned != 0 {
		t.Fatalf("deleted row = %s/%d", status, pinned)
	}
	// The remaining pinned gift moved into the vacated slot.
	if err := pool.QueryRow(ctx, `SELECT pinned_order FROM peer_star_gifts WHERE owner_peer_id=$1 AND msg_id=2`, owner.ID).Scan(&pinned); err != nil || pinned != 1 {
		t.Fatalf("remaining pin = %d, %v; want 1", pinned, err)
	}
	if n, err := s.CountByOwner(ctx, peer); err != nil || n != 1 {
		t.Fatalf("count after delete = %d, %v; want 1", n, err)
	}
	if err := s.DeleteSaved(ctx, ref(1)); !errors.Is(err, domain.ErrStarGiftNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
	if err := s.DeleteSaved(ctx, ref(3)); !errors.Is(err, domain.ErrStarGiftUnavailable) {
		t.Fatalf("converted delete err = %v", err)
	}
	if err := s.DeleteSaved(ctx, ref(99)); !errors.Is(err, domain.ErrStarGiftNotFound) {
		t.Fatalf("missing delete err = %v", err)
	}
	// The schema refuses a deleted unique gift row.
	if _, err := pool.Exec(ctx, `UPDATE peer_star_gifts SET lifecycle_status='deleted', converted=true WHERE owner_peer_id=$1 AND msg_id=2`, owner.ID); err == nil {
		t.Fatal("constraint accepted a converted+deleted row")
	}
}
