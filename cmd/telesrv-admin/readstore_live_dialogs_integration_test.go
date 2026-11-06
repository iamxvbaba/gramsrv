package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The live console pages a user's dialogs with a (top_message_date, peer_id)
// keyset cursor. Ties on the date and dialogs with no top message (date 0, left
// by a scheduled message, a TTL or a chat theme) are where a cursor repeats or
// loses rows, so walk every page against the real schema. Gated on
// TELESRV_TEST_POSTGRES_DSN like the rest of this package's integration tests.
func TestReadStoreListDialogsForUserPagesWithTiesAndZeroDates(t *testing.T) {
	store, pool := verificationReadStore(t)
	ctx := context.Background()
	unique := time.Now().UnixNano() % 1_000_000
	userID := 3_800_000_000 + unique

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM dialogs WHERE user_id=$1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, access_hash, phone, first_name, last_name, username, created_at, updated_at)
VALUES ($1, $2, $3, 'Reviewed', '', $4, now(), now())`,
		userID, userID, fmt.Sprintf("+1887%d", unique), fmt.Sprintf("reviewed%d", unique)); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Peer ids are deliberately not in date order, so only the peer_id
	// tie-breaker fixes the order inside a date.
	dialogs := []struct {
		peerID int64
		date   int
	}{
		{104, 500},
		{101, 400}, {107, 400}, {105, 400},
		{103, 300},
		{102, 0}, {108, 0}, {106, 0},
	}
	for _, d := range dialogs {
		if _, err := pool.Exec(ctx, `
INSERT INTO dialogs (user_id, peer_type, peer_id, top_message_id, top_message_date)
VALUES ($1, 'user', $2, $3, $4)`, userID, d.peerID, d.date/100, d.date); err != nil {
			t.Fatalf("seed dialog %d: %v", d.peerID, err)
		}
	}
	want := []string{"500/104", "400/107", "400/105", "400/101", "300/103", "0/108", "0/106", "0/102"}

	for _, pageSize := range []int{2, 3} {
		var got []string
		var beforeDate, beforePeerID int64
		for page := 0; ; page++ {
			if page > len(want) {
				t.Fatalf("page size %d: no last page after %d pages, got %v", pageSize, page, got)
			}
			rows, hasMore, err := store.ListDialogsForUser(ctx, userID, beforeDate, beforePeerID, pageSize)
			if err != nil {
				t.Fatalf("page size %d page %d: %v", pageSize, page, err)
			}
			for _, row := range rows {
				got = append(got, fmt.Sprintf("%d/%d", row.TopMessageDate, row.PeerID))
			}
			last := len(got) >= len(want)
			if hasMore == last {
				t.Fatalf("page size %d page %d: has_more=%v after %d of %d rows", pageSize, page, hasMore, len(got), len(want))
			}
			if !hasMore {
				break
			}
			if len(rows) != pageSize {
				t.Fatalf("page size %d page %d: %d rows on a page that is not the last", pageSize, page, len(rows))
			}
			tail := rows[len(rows)-1]
			beforeDate, beforePeerID = int64(tail.TopMessageDate), tail.PeerID
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("page size %d walked %v, want %v", pageSize, got, want)
		}
	}
}
