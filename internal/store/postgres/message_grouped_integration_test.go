package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestSendPrivateGroupedIDSurvivesReadPaths regresses migration 0008: the album grouped_id
// stays faithful through all private read paths — the send result's two boxes, history, GetByIDs,
// and the update-event rebuild path (shared by offline difference / online push). This is the most
// easily missed spot for "one missing read site = grouping silently lost".
func TestSendPrivateGroupedIDSurvivesReadPaths(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{AccessHash: 81, Phone: "+1995" + suffix + "01", FirstName: "AlbumSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 82, Phone: "+1995" + suffix + "02", FirstName: "AlbumRecipient"})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	ids := []int64{sender.ID, recipient.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM user_update_events WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM message_boxes WHERE owner_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM private_messages WHERE sender_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM dialogs WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", ids)
	})

	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	const groupedID = int64(880000000123)

	// The two messages of the same album share grouped_id.
	var senderMsgIDs []int
	for i := 0; i < 2; i++ {
		res, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    sender.ID,
			RecipientUserID: recipient.ID,
			RandomID:        time.Now().UnixNano() + int64(i),
			Message:         "album item",
			GroupedID:       groupedID,
			Date:            int(time.Now().Unix()),
		})
		if err != nil {
			t.Fatalf("send album item %d: %v", i, err)
		}
		if res.SenderMessage.GroupedID != groupedID || res.RecipientMessage.GroupedID != groupedID {
			t.Fatalf("send result grouped = sender %d recipient %d, want %d", res.SenderMessage.GroupedID, res.RecipientMessage.GroupedID, groupedID)
		}
		senderMsgIDs = append(senderMsgIDs, res.SenderMessage.ID)
	}

	// history: both carry the same grouped_id (the client groups by it).
	hist, err := messages.ListByUser(ctx, recipient.ID, domain.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("recipient history: %v", err)
	}
	album := 0
	for _, m := range hist.Messages {
		if m.GroupedID == groupedID {
			album++
		}
	}
	if album != 2 {
		t.Fatalf("recipient history grouped count = %d, want 2", album)
	}

	// GetByIDs carries grouped_id.
	byID, err := messages.GetByIDs(ctx, sender.ID, senderMsgIDs)
	if err != nil {
		t.Fatalf("sender get by ids: %v", err)
	}
	if len(byID.Messages) != 2 {
		t.Fatalf("get by ids = %d, want 2", len(byID.Messages))
	}
	for _, m := range byID.Messages {
		if m.GroupedID != groupedID {
			t.Fatalf("get-by-id grouped = %d, want %d", m.GroupedID, groupedID)
		}
	}

	// The update-event rebuild path (key: shared by getDifference/push) carries grouped_id.
	events := NewUpdateEventStore(pool)
	got, err := events.ListAfter(ctx, recipient.ID, 0, 20)
	if err != nil {
		t.Fatalf("list recipient events: %v", err)
	}
	rebuilt := 0
	for _, ev := range got {
		if ev.Type == domain.UpdateEventNewMessage && ev.Message.ID != 0 {
			if ev.Message.GroupedID != groupedID {
				t.Fatalf("update-event grouped = %d, want %d (rebuild path lost the grouping)", ev.Message.GroupedID, groupedID)
			}
			rebuilt++
		}
	}
	if rebuilt != 2 {
		t.Fatalf("rebuilt new_message events with grouped = %d, want 2", rebuilt)
	}
}

// TestSendChannelGroupedIDSurvivesReadPaths regresses migration 0008: the channel-side (hand-written
// SQL) album grouped_id stays faithful through the send result/events/history/GetChannelMessages.
func TestSendChannelGroupedIDSurvivesReadPaths(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 3201, Phone: "+1887" + suffix + "01", FirstName: "AlbumChannelOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Album Channel " + suffix,
		Megagroup:     true,
		Date:          1700002100,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	const groupedID = int64(990000000456)

	var msgIDs []int
	for i := 0; i < 2; i++ {
		sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    owner.ID,
			ChannelID: channelID,
			RandomID:  int64(990401 + i),
			Message:   "channel album item",
			GroupedID: groupedID,
			Date:      1700002101 + i,
		})
		if err != nil {
			t.Fatalf("send channel album item %d: %v", i, err)
		}
		if sent.Message.GroupedID != groupedID || sent.Event.Message.GroupedID != groupedID {
			t.Fatalf("sent grouped = msg %d event %d, want %d", sent.Message.GroupedID, sent.Event.Message.GroupedID, groupedID)
		}
		msgIDs = append(msgIDs, sent.Message.ID)
	}

	hist, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 10})
	if err != nil {
		t.Fatalf("channel history: %v", err)
	}
	album := 0
	for _, m := range hist.Messages {
		if m.GroupedID == groupedID {
			album++
		}
	}
	if album != 2 {
		t.Fatalf("channel history grouped count = %d, want 2", album)
	}

	byID, err := channels.GetChannelMessages(ctx, owner.ID, channelID, msgIDs)
	if err != nil {
		t.Fatalf("get channel messages: %v", err)
	}
	if len(byID.Messages) != 2 {
		t.Fatalf("get channel messages = %d, want 2", len(byID.Messages))
	}
	for _, m := range byID.Messages {
		if m.GroupedID != groupedID {
			t.Fatalf("channel get-by-id grouped = %d, want %d", m.GroupedID, groupedID)
		}
	}
}
