package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The Accounts tab is hand-written SQL, so the collectible-username aggregation
// can only be proven against the real schema: the jsonb keys have to match the
// AccountUsername field names for pgx to unmarshal them, the ordering has to match
// the projection order clients see, and the editable slot must not leak into the
// collectible list. Gated on TELESRV_TEST_POSTGRES_DSN like the rest.
func TestReadStoreAccountsCarryCollectibleUsernames(t *testing.T) {
	store, pool := verificationReadStore(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	userID := 3_600_000_000 + time.Now().UnixNano()%1_000_000

	editable := "slot" + suffix
	// Deliberately out of alphabetical order and with a gap in sort_order, so a
	// query that sorted by name or by insertion order would produce a different
	// answer than the stored one.
	collectibles := []struct {
		name       string
		sortOrder  int
		active     bool
		collecting bool
	}{
		{name: "zeta" + suffix, sortOrder: 0, active: true, collecting: true},
		{name: "alpha" + suffix, sortOrder: 5, active: false, collecting: true},
		{name: "mid" + suffix, sortOrder: 2, active: true, collecting: true},
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM peer_usernames WHERE peer_type='user' AND peer_id=$1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM collectible_usernames WHERE username_lower LIKE $1`, "%"+suffix)
		_, _ = pool.Exec(ctx, `DELETE FROM authorizations WHERE user_id=$1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM auth_keys WHERE auth_key_id=$1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})

	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, access_hash, phone, first_name, last_name, username, created_at, updated_at)
VALUES ($1, $2, $3, 'Collector', '', $4, now(), now())`,
		userID, userID, "+1889"+suffix, editable); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// The list query joins authorizations, so an account with no device never
	// appears there at all; an authorization in turn needs its auth key to exist.
	if _, err := pool.Exec(ctx, `
INSERT INTO auth_keys (auth_key_id, body, server_salt) VALUES ($1, '\x00', 0)`, userID); err != nil {
		t.Fatalf("seed auth key: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO authorizations (user_id, auth_key_id, created_at, active_at)
VALUES ($1, $2, now(), now())`, userID, userID); err != nil {
		t.Fatalf("seed authorization: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO peer_usernames (username_lower, username, peer_type, peer_id, active, editable, sort_order)
VALUES (lower($1), lower($1), 'user', $2, true, true, 0)`, editable, userID); err != nil {
		t.Fatalf("seed editable slot: %v", err)
	}
	for _, item := range collectibles {
		var collectibleID int64
		if err := pool.QueryRow(ctx, `
INSERT INTO collectible_usernames (username, username_lower, status, owner_peer_type, owner_peer_id,
	original_owner_peer_type, original_owner_peer_id, purchase_date, currency, amount, created_at, updated_at)
VALUES ($1, lower($1), 'owned', 'user', $2, 'user', $2, now(), 'XTR', 0, now(), now())
RETURNING id`, item.name, userID).Scan(&collectibleID); err != nil {
			t.Fatalf("seed collectible %s: %v", item.name, err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO peer_usernames (username_lower, username, peer_type, peer_id, active, editable, sort_order, collectible_id)
VALUES (lower($1), lower($1), 'user', $2, $3, false, $4, $5)`,
			item.name, userID, item.active, item.sortOrder, collectibleID); err != nil {
			t.Fatalf("attach collectible %s: %v", item.name, err)
		}
	}

	want := []AccountUsername{
		{Username: "zeta" + suffix, Active: true},
		{Username: "mid" + suffix, Active: true},
		{Username: "alpha" + suffix, Active: false},
	}

	detail, err := store.AccountDetail(ctx, userID)
	if err != nil {
		t.Fatalf("AccountDetail: %v", err)
	}
	assertCollectibles(t, "AccountDetail", detail.Account, editable, want)

	rows, _, err := store.ListAccounts(ctx, 0, 0, 200)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	var listed *AccountRow
	for i := range rows {
		if rows[i].ID == userID {
			listed = &rows[i]
			break
		}
	}
	if listed == nil {
		t.Fatalf("seeded account %d is absent from the first page of %d accounts", userID, len(rows))
	}
	assertCollectibles(t, "ListAccounts", *listed, editable, want)

	// An account holding nothing collectible reports an empty list, not null: the
	// panel iterates it unconditionally.
	if _, err := pool.Exec(ctx, `DELETE FROM peer_usernames
WHERE peer_type='user' AND peer_id=$1 AND collectible_id IS NOT NULL`, userID); err != nil {
		t.Fatalf("drop collectibles: %v", err)
	}
	bare, err := store.AccountDetail(ctx, userID)
	if err != nil {
		t.Fatalf("AccountDetail without collectibles: %v", err)
	}
	if bare.Account.Collectibles == nil || len(bare.Account.Collectibles) != 0 {
		t.Fatalf("collectibles without any rows = %#v, want an empty slice", bare.Account.Collectibles)
	}
}

func assertCollectibles(t *testing.T, surface string, row AccountRow, editable string, want []AccountUsername) {
	t.Helper()
	if row.Username != editable {
		t.Fatalf("%s: editable username = %q, want %q", surface, row.Username, editable)
	}
	if len(row.Collectibles) != len(want) {
		t.Fatalf("%s: collectibles = %#v, want %#v", surface, row.Collectibles, want)
	}
	for i := range want {
		if row.Collectibles[i] != want[i] {
			t.Fatalf("%s: collectibles = %#v, want %#v", surface, row.Collectibles, want)
		}
	}
	// The editable slot is a different kind of row and must never be repeated in
	// the collectible list.
	for _, item := range row.Collectibles {
		if item.Username == editable {
			t.Fatalf("%s: editable slot leaked into the collectible list: %#v", surface, row.Collectibles)
		}
	}
}
