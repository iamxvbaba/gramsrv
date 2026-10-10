package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestAccountDeletionRetiresIdentityPostgres pins the identity half of a logical
// account deletion: the ordinary username is retired forever, NFT usernames go
// back to storage, and the avatar is deactivated so a deleted account has
// nothing left to project but its ghost.
func TestAccountDeletionRetiresIdentityPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	nonce := time.Now().UnixNano()
	users := NewUserStore(pool)
	deleted := createTestUser(t, ctx, users, fmt.Sprintf("15581%d", nonce), "Ghost", "Owner")
	peer := createTestUser(t, ctx, users, fmt.Sprintf("15582%d", nonce), "Claim", "Ender")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM collectible_username_transfers WHERE collectible_id IN (
SELECT id FROM collectible_usernames WHERE username_lower LIKE 'frag$1')`, nonce%1_000_000_000)
		_, _ = pool.Exec(ctx, `DELETE FROM peer_usernames WHERE peer_type = 'user' AND peer_id = ANY($1)`, []int64{deleted.ID, peer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM collectible_usernames WHERE username_lower LIKE 'frag$1'`, nonce%1_000_000_000)
		_, _ = pool.Exec(ctx, `DELETE FROM profile_photos WHERE owner_peer_type = 'user' AND owner_peer_id = ANY($1)`, []int64{deleted.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM photos WHERE id IN (
SELECT photo_id FROM profile_photos WHERE owner_peer_type = 'user' AND owner_peer_id = ANY($1))`, []int64{deleted.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM account_deletion_requests WHERE user_id = ANY($1)`, []int64{deleted.ID})
		// Deletion enqueues one tombstone notification per contact/dialog peer.
		// The queue is global and its FK to users has no cascade, so leaking rows
		// here would both block the user delete and pollute every later claim.
		_, _ = pool.Exec(ctx, `DELETE FROM account_deletion_notifications
WHERE deleted_user_id = ANY($1) OR target_user_id = ANY($1)`, []int64{deleted.ID, peer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []int64{deleted.ID, peer.ID})
	})

	username := fmt.Sprintf("ghostname%d", nonce)
	var updated domain.User
	var err error
	if updated, err = users.UpdateUsername(ctx, deleted.ID, username); err != nil {
		t.Fatalf("set username: %v", err)
	}
	collectibleName := fmt.Sprintf("frag%d", nonce%1_000_000_000)
	collectibles := NewCollectibleUsernameStore(pool)
	asset, created, err := collectibles.MintCollectibleUsername(ctx, domain.MintCollectibleUsernameRequest{
		Username:     collectibleName,
		Owner:        domain.Peer{Type: domain.PeerTypeUser, ID: updated.ID},
		PurchaseDate: time.Now().UTC(),
		Currency:     "XTR",
		Amount:       100,
		Actor:        "test",
	})
	if err != nil || !created {
		t.Fatalf("mint collectible username = %+v created=%v err=%v", asset, created, err)
	}
	if _, found, err := users.ByUsername(ctx, collectibleName); err != nil || !found {
		t.Fatalf("collectible username before deletion found=%v err=%v", found, err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO photos (id, date, dc_id, sizes) VALUES ($1, 0, 2, '[]'::jsonb)`,
		int64(nonce%1_000_000_000)+900000); err != nil {
		t.Fatalf("insert photo: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO profile_photos (owner_peer_type, owner_peer_id, photo_id, date, active, sort_order, kind)
VALUES ('user', $1, $2, 0, true, 0, 'profile')`, deleted.ID, int64(nonce%1_000_000_000)+900000); err != nil {
		t.Fatalf("insert profile photo: %v", err)
	}

	result, err := NewAccountLifecycleStore(pool).ExecuteAccountDeletion(
		ctx, deleted.ID, domain.AccountDeletionManual, "manual", time.Now().UTC().Truncate(time.Second))
	if err != nil {
		t.Fatalf("execute account deletion: %v", err)
	}
	if result.VaultedCollectibleUsernames != 1 {
		t.Fatalf("vaulted collectible usernames = %d, want 1", result.VaultedCollectibleUsernames)
	}

	// The name is gone from every lookup but stays claimed forever.
	if _, found, err := users.ByUsername(ctx, username); err != nil || found {
		t.Fatalf("deleted username resolves found=%v err=%v", found, err)
	}
	if available, err := users.CheckUsername(ctx, peer.ID, username); err != nil || available {
		t.Fatalf("deleted username available=%v err=%v, want permanently occupied", available, err)
	}
	if _, err := users.UpdateUsername(ctx, peer.ID, username); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("claim deleted username err=%v, want ErrUsernameOccupied", err)
	}
	var active bool
	if err := pool.QueryRow(ctx, `
SELECT active FROM peer_usernames WHERE peer_type = 'user' AND peer_id = $1 AND editable`, deleted.ID).Scan(&active); err != nil {
		t.Fatalf("read retired registry row: %v", err)
	}
	if active {
		t.Fatal("retired registry row is still active, the name would resolve to the tombstone")
	}

	// The NFT username is back in storage: unowned, unresolvable, still issuable.
	var status, ownerType string
	var ownerID int64
	if err := pool.QueryRow(ctx, `
SELECT status, owner_peer_type, owner_peer_id FROM collectible_usernames WHERE id = $1`, asset.ID).
		Scan(&status, &ownerType, &ownerID); err != nil {
		t.Fatalf("read vaulted asset: %v", err)
	}
	if status != "vault" || ownerType != "" || ownerID != 0 {
		t.Fatalf("collectible asset = %q/%q/%d, want vault with no owner", status, ownerType, ownerID)
	}
	if _, found, err := users.ByUsername(ctx, collectibleName); err != nil || found {
		t.Fatalf("collectible username still resolves found=%v err=%v", found, err)
	}
	var registryRows int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM peer_usernames WHERE collectible_id = $1`, asset.ID).Scan(&registryRows); err != nil {
		t.Fatalf("count collectible registry rows: %v", err)
	}
	if registryRows != 0 {
		t.Fatalf("collectible registry rows = %d, want 0", registryRows)
	}
	var kind, actor string
	if err := pool.QueryRow(ctx, `
SELECT kind, actor FROM collectible_username_transfers
WHERE collectible_id = $1 ORDER BY id DESC LIMIT 1`, asset.ID).Scan(&kind, &actor); err != nil {
		t.Fatalf("read provenance row: %v", err)
	}
	if kind != "revoke" || actor != collectibleUsernameDeletionActor {
		t.Fatalf("provenance row = %q/%q, want revoke by the deletion boundary", kind, actor)
	}

	// No avatar survives: the row stays for history, but nothing can project it.
	if current, found, err := NewMediaStore(pool).CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, deleted.ID, domain.ProfilePhotoKindProfile); err != nil {
		t.Fatalf("read current profile photo: %v", err)
	} else if found {
		t.Fatalf("deleted account still projects profile photo %d", current)
	}
}
