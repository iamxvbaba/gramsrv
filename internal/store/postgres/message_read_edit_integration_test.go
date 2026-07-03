package postgres

import (
	"context"
	"telesrv/internal/domain"
	"testing"
)

func TestMessageStoreReadAndEditEmitDurableEvents(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{
		AccessHash: 31,
		Phone:      "+1666" + suffix + "11",
		FirstName:  "ReadSender",
	})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 32,
		Phone:      "+1666" + suffix + "12",
		FirstName:  "ReadRecipient",
	})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        223344,
		Message:         "before edit",
		Date:            1700000300,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	read, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: recipient.ID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID},
		Date:        1700000310,
	})
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if !read.Changed || read.InboxEvent.Pts != 2 || read.InboxEvent.Type != domain.UpdateEventReadHistoryInbox || read.InboxEvent.MaxID != sent.RecipientMessage.ID {
		t.Fatalf("read inbox = %+v, want recipient pts=2 max recipient id", read)
	}
	if !read.OutboxChanged || read.OutboxEvent.Pts != 2 || read.OutboxEvent.Type != domain.UpdateEventReadHistoryOutbox || read.OutboxEvent.MaxID != sent.SenderMessage.ID {
		t.Fatalf("read outbox = %+v, want sender pts=2 max sender id", read)
	}
	readDate, err := messages.GetOutboxReadDate(ctx, domain.OutboxReadDateRequest{
		OwnerUserID: sender.ID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID},
		ID:          sent.SenderMessage.ID,
	})
	if err != nil || readDate != 1700000310 {
		t.Fatalf("outbox read date = %d err=%v, want read date", readDate, err)
	}

	edited, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID: sender.ID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID},
		ID:          sent.SenderMessage.ID,
		Message:     "after edit",
		EditDate:    1700000320,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if self := edited.Self(); self.Event.Pts != 3 || self.Event.Type != domain.UpdateEventEditMessage || self.Message.Body != "after edit" {
		t.Fatalf("self edit = %+v, want sender edit event pts=3", self)
	}
	recipientHistory, err := messages.ListByUser(ctx, recipient.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("recipient history: %v", err)
	}
	if len(recipientHistory.Messages) != 1 || recipientHistory.Messages[0].Body != "after edit" || recipientHistory.Messages[0].EditDate != 1700000320 {
		t.Fatalf("recipient history = %+v, want edited message visible", recipientHistory.Messages)
	}

	senderEvents, err := NewUpdateEventStore(pool).ListAfter(ctx, sender.ID, 0, 10)
	if err != nil {
		t.Fatalf("sender events: %v", err)
	}
	if len(senderEvents) != 3 || senderEvents[1].Type != domain.UpdateEventReadHistoryOutbox || senderEvents[2].Type != domain.UpdateEventEditMessage {
		t.Fatalf("sender events = %+v, want new/read_outbox/edit", senderEvents)
	}
	recipientEvents, err := NewUpdateEventStore(pool).ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("recipient events: %v", err)
	}
	if len(recipientEvents) != 3 || recipientEvents[1].Type != domain.UpdateEventReadHistoryInbox || recipientEvents[2].Type != domain.UpdateEventEditMessage || recipientEvents[2].Message.Body != "after edit" {
		t.Fatalf("recipient events = %+v, want new/read_inbox/edit with edited body", recipientEvents)
	}
}

func TestMessageStoreEditCanHideEditedBadge(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{
		AccessHash: 331,
		Phone:      "+1666" + suffix + "31",
		FirstName:  "HiddenEditSender",
	})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 332,
		Phone:      "+1666" + suffix + "32",
		FirstName:  "HiddenEditRecipient",
	})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        223399,
		Message:         "...",
		Date:            1700000400,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	edited, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID: sender.ID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID},
		ID:          sent.SenderMessage.ID,
		Message:     "streamed answer",
		EditDate:    1700000405,
		HideEdited:  true,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if self := edited.Self(); self.Message.Body != "streamed answer" || !self.Message.HideEdited {
		t.Fatalf("self hidden edit = %+v, want hidden edited message", self)
	}

	history, err := messages.ListByUser(ctx, recipient.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("recipient history: %v", err)
	}
	if len(history.Messages) != 1 || history.Messages[0].Body != "streamed answer" || !history.Messages[0].HideEdited {
		t.Fatalf("recipient history = %+v, want hidden edited message", history.Messages)
	}

	events, err := NewUpdateEventStore(pool).ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("recipient events: %v", err)
	}
	if len(events) != 2 || events[1].Type != domain.UpdateEventEditMessage || !events[1].Message.HideEdited {
		t.Fatalf("recipient events = %+v, want hidden edit event", events)
	}
}

func TestMessageStoreReadHistoryStaleUnreadRepairDoesNotAppendPts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{
		AccessHash: 41,
		Phone:      "+1666" + suffix + "21",
		FirstName:  "StaleReadSender",
	})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 42,
		Phone:      "+1666" + suffix + "22",
		FirstName:  "StaleReadRecipient",
	})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID}
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        323344,
		Message:         "before stale repair",
		Date:            1700000300,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	if _, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: recipient.ID,
		Peer:        peer,
		MaxID:       sent.RecipientMessage.ID,
		Date:        1700000310,
	}); err != nil {
		t.Fatalf("first ReadHistory: %v", err)
	}
	eventsBefore, err := NewUpdateEventStore(pool).ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("list events before repair: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE dialogs
		SET unread_count = 1,
		    unread_mentions_count = 1,
		    unread_reactions_count = 1,
		    unread_mark = true
		WHERE user_id = $1
		  AND peer_type = $2
		  AND peer_id = $3
	`, recipient.ID, string(domain.PeerTypeUser), sender.ID); err != nil {
		t.Fatalf("corrupt unread count: %v", err)
	}

	read, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: recipient.ID,
		Peer:        peer,
		MaxID:       sent.RecipientMessage.ID,
		Date:        1700000320,
	})
	if err != nil {
		t.Fatalf("second ReadHistory: %v", err)
	}
	if read.Changed || read.InboxEvent.Pts != 0 || read.OutboxChanged {
		t.Fatalf("stale unread repair = %+v, want no read pts/outbox event", read)
	}
	eventsAfter, err := NewUpdateEventStore(pool).ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("list events after repair: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("recipient events after repair = %d, want unchanged %d", len(eventsAfter), len(eventsBefore))
	}
	var unreadCount, unreadMentions, unreadReactions int
	var unreadMark bool
	if err := pool.QueryRow(ctx, `
		SELECT unread_count, unread_mentions_count, unread_reactions_count, unread_mark
		FROM dialogs
		WHERE user_id = $1
		  AND peer_type = $2
		  AND peer_id = $3
	`, recipient.ID, string(domain.PeerTypeUser), sender.ID).Scan(&unreadCount, &unreadMentions, &unreadReactions, &unreadMark); err != nil {
		t.Fatalf("load repaired dialog: %v", err)
	}
	// readHistory 只清 message unread/mentions/manual mark；reaction 角标
	// 由 messages.readReactions 单独清除，与官方语义一致。
	if unreadCount != 0 || unreadMentions != 0 || unreadReactions != 1 || unreadMark {
		t.Fatalf("dialog unread fields = count %d mentions %d reactions %d mark %v, want history cleared with reaction badge preserved", unreadCount, unreadMentions, unreadReactions, unreadMark)
	}
	// stale counter 场景没有存活的 reaction_unread 行，readReactions 清 0
	// 行但仍要把角标计数自愈归零。
	if cleared, err := messages.ReadPeerReactions(ctx, recipient.ID, domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID}); err != nil || cleared != 0 {
		t.Fatalf("ReadPeerReactions = %d err %v, want stale repair without live rows", cleared, err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT unread_reactions_count
		FROM dialogs
		WHERE user_id = $1 AND peer_type = $2 AND peer_id = $3
	`, recipient.ID, string(domain.PeerTypeUser), sender.ID).Scan(&unreadReactions); err != nil {
		t.Fatalf("load dialog after readReactions: %v", err)
	}
	if unreadReactions != 0 {
		t.Fatalf("unread reactions after readReactions = %d, want 0", unreadReactions)
	}
	next, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        323345,
		Message:         "after stale repair",
		Date:            1700000330,
	})
	if err != nil {
		t.Fatalf("next SendPrivateText: %v", err)
	}
	if next.RecipientMessage.Pts != 3 {
		t.Fatalf("next recipient pts = %d, want 3 after no-op read repair", next.RecipientMessage.Pts)
	}
}

func TestMessageStoreReadHistoryClampsFutureMaxIDAndRepairsDialog(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{
		AccessHash: 43,
		Phone:      "+1667" + suffix + "11",
		FirstName:  "FutureReadSender",
	})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 44,
		Phone:      "+1667" + suffix + "12",
		FirstName:  "FutureReadRecipient",
	})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID}
	first, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        823101,
		Message:         "first",
		Date:            1700000500,
	})
	if err != nil {
		t.Fatalf("SendPrivateText first: %v", err)
	}
	read, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: recipient.ID,
		Peer:        peer,
		MaxID:       domain.MaxMessageBoxID,
		Date:        1700000510,
	})
	if err != nil {
		t.Fatalf("ReadHistory future max: %v", err)
	}
	if read.MaxID != first.RecipientMessage.ID || read.InboxEvent.MaxID != first.RecipientMessage.ID {
		t.Fatalf("read = %+v, want max clamped to current top %d", read, first.RecipientMessage.ID)
	}

	second, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        823102,
		Message:         "second",
		Date:            1700000520,
	})
	if err != nil {
		t.Fatalf("SendPrivateText second: %v", err)
	}
	var unreadCount, readInboxMaxID, topMessageID int
	if err := pool.QueryRow(ctx, `
		SELECT unread_count, read_inbox_max_id, top_message_id
		FROM dialogs
		WHERE user_id = $1
		  AND peer_type = $2
		  AND peer_id = $3
	`, recipient.ID, string(domain.PeerTypeUser), sender.ID).Scan(&unreadCount, &readInboxMaxID, &topMessageID); err != nil {
		t.Fatalf("load dialog after second send: %v", err)
	}
	if unreadCount != 1 || readInboxMaxID != first.RecipientMessage.ID || topMessageID != second.RecipientMessage.ID {
		t.Fatalf("dialog after second = unread %d read %d top %d, want unread=1 read=%d top=%d",
			unreadCount, readInboxMaxID, topMessageID, first.RecipientMessage.ID, second.RecipientMessage.ID)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE dialogs
		SET read_inbox_max_id = $4,
		    unread_count = 0
		WHERE user_id = $1
		  AND peer_type = $2
		  AND peer_id = $3
	`, recipient.ID, string(domain.PeerTypeUser), sender.ID, domain.MaxMessageBoxID); err != nil {
		t.Fatalf("corrupt future read watermark: %v", err)
	}
	if _, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: recipient.ID,
		Peer:        peer,
		MaxID:       domain.MaxMessageBoxID,
		Date:        1700000530,
	}); err != nil {
		t.Fatalf("ReadHistory repair future watermark: %v", err)
	}
	third, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        823103,
		Message:         "third",
		Date:            1700000540,
	})
	if err != nil {
		t.Fatalf("SendPrivateText third: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT unread_count, read_inbox_max_id, top_message_id
		FROM dialogs
		WHERE user_id = $1
		  AND peer_type = $2
		  AND peer_id = $3
	`, recipient.ID, string(domain.PeerTypeUser), sender.ID).Scan(&unreadCount, &readInboxMaxID, &topMessageID); err != nil {
		t.Fatalf("load dialog after third send: %v", err)
	}
	if unreadCount != 1 || readInboxMaxID != second.RecipientMessage.ID || topMessageID != third.RecipientMessage.ID {
		t.Fatalf("dialog after repair+third = unread %d read %d top %d, want unread=1 read=%d top=%d",
			unreadCount, readInboxMaxID, topMessageID, second.RecipientMessage.ID, third.RecipientMessage.ID)
	}
}
