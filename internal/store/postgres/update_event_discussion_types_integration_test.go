package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	storepkg "telesrv/internal/store"
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
	type discussionWant struct {
		eventType domain.UpdateEventType
		topMsgID  int
		maxID     int
		pts       int
	}
	wants := []discussionWant{
		{eventType: domain.UpdateEventReadChannelDiscussionInbox, topMsgID: 51, maxID: 100},
		{eventType: domain.UpdateEventReadChannelDiscussionOutbox, topMsgID: 52, maxID: 101},
	}
	for i := range wants {
		want := &wants[i]
		et := want.eventType
		ev, aerr := store.AppendAllocated(ctx, u.ID, domain.UpdateEvent{
			Type:     et,
			Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
			TopMsgID: want.topMsgID,
			MaxID:    want.maxID,
			PtsCount: 1,
			Date:     1700000900 + i,
		})
		if aerr != nil {
			t.Fatalf("append %s failed (is the type missing from migration 0003's constraint whitelist?): %v", et, aerr)
		}
		if ev.Pts <= 0 || ev.PtsCount != 1 {
			t.Fatalf("append %s: pts=%d pts_count=%d, want one allocated account PTS", et, ev.Pts, ev.PtsCount)
		}
		want.pts = ev.Pts
	}

	events, err := store.ListAfter(ctx, u.ID, 0, 50)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	seen := make(map[domain.UpdateEventType]domain.UpdateEvent, len(events))
	for _, event := range events {
		seen[event.Type] = event
	}
	for _, want := range wants {
		got, ok := seen[want.eventType]
		if !ok {
			t.Fatalf("discussion event %s missing from ListAfter: seen=%v", want.eventType, seen)
		}
		if got.TopMsgID != want.topMsgID || got.MaxID != want.maxID || got.Pts != want.pts || got.PtsCount != 1 {
			t.Fatalf("ListAfter %s = top_msg_id=%d max_id=%d pts=%d/%d, want %d/%d pts=%d/1",
				want.eventType, got.TopMsgID, got.MaxID, got.Pts, got.PtsCount,
				want.topMsgID, want.maxID, want.pts)
		}
	}

	cursors := make([]storepkg.EventCursor, len(wants))
	for i, want := range wants {
		cursors[i] = storepkg.EventCursor{UserID: u.ID, Pts: want.pts}
	}
	batched, err := store.BatchByCursor(ctx, cursors)
	if err != nil {
		t.Fatalf("batch by cursor: %v", err)
	}
	batchedByType := make(map[domain.UpdateEventType]domain.UpdateEvent, len(batched))
	for _, event := range batched {
		batchedByType[event.Type] = event
	}
	for _, want := range wants {
		got, ok := batchedByType[want.eventType]
		if !ok || got.TopMsgID != want.topMsgID || got.MaxID != want.maxID || got.Pts != want.pts {
			t.Fatalf("BatchByCursor %s = %+v, want top_msg_id=%d max_id=%d pts=%d",
				want.eventType, got, want.topMsgID, want.maxID, want.pts)
		}
	}
	if len(wants) != 2 || wants[1].pts != wants[0].pts+1 {
		t.Fatalf("payload persistence changed PTS allocation: %+v", wants)
	}
}
