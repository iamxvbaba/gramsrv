package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestUpdateEventAppendDiscussionTypes regresses migration 0003: the forum topic-read durable event
// types read_channel_discussion_inbox / read_channel_discussion_outbox must be writable into user_update_events.
// 0001's user_update_events_type_check whitelist originally missed these two types (0002/a180fbc added
// the topic-read feature without adding the constraint), so on a real PostgreSQL the first
// messages.readDiscussion topic-read append of a durable event was rejected by the CHECK constraint
// (SQLSTATE 23514) -> RPC 500, and the topic read never entered durable / getDifference.
// The memory store has no CHECK constraint so unit tests did not expose it; only a real two-machine DrKLO run found it.
func TestUpdateEventAppendDiscussionTypes(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	u, err := users.Create(ctx, domain.User{AccessHash: 71, Phone: "+1675" + suffix + "01", FirstName: "DiscRead"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", u.ID) })

	store := NewUpdateEventStore(pool)
	const channelID = int64(990077)
	for _, et := range []domain.UpdateEventType{
		domain.UpdateEventReadChannelDiscussionInbox,
		domain.UpdateEventReadChannelDiscussionOutbox,
	} {
		ev, aerr := store.AppendAllocated(ctx, u.ID, domain.UpdateEvent{
			Type:     et,
			Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
			TopMsgID: 51,
			MaxID:    100,
			PtsCount: 1,
			Date:     1700000900,
		})
		if aerr != nil {
			t.Fatalf("append %s failed (is the type missing from migration 0003's constraint whitelist?): %v", et, aerr)
		}
		if ev.Pts <= 0 {
			t.Fatalf("append %s: pts=%d, want >0", et, ev.Pts)
		}
	}

	events, err := store.ListAfter(ctx, u.ID, 0, 50)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	seen := map[domain.UpdateEventType]bool{}
	for _, e := range events {
		seen[e.Type] = true
	}
	if !seen[domain.UpdateEventReadChannelDiscussionInbox] || !seen[domain.UpdateEventReadChannelDiscussionOutbox] {
		t.Fatalf("discussion read events cannot be replayed via ListAfter (multi-device getDifference sync missing): seen=%v", seen)
	}
}
