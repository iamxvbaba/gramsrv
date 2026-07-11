package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestMessageStoreSendPrivateTextRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{
		AccessHash: 11,
		Phone:      "+1666" + suffix + "01",
		FirstName:  "Sender",
	})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 22,
		Phone:      "+1666" + suffix + "02",
		FirstName:  "Recipient",
	})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	var originAuthKeyID [8]byte
	originAuthKeyID[0] = 5
	req := domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        123456,
		Message:         "hello from pg",
		Entities: []domain.MessageEntity{
			{Type: domain.MessageEntityBold, Offset: 0, Length: 5},
			{Type: domain.MessageEntityFormattedDate, Offset: 6, Length: 4, Date: 1773436800, ShortTime: true, LongDate: true},
		},
		Date:            1700000200,
		OriginAuthKeyID: originAuthKeyID,
		OriginSessionID: 77,
	}
	got, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	if got.SenderMessage.ID != 1 || got.SenderMessage.Pts != 1 || !got.SenderMessage.Out || got.SenderMessage.Peer.ID != recipient.ID {
		t.Fatalf("sender message = %+v, want first outgoing box to recipient", got.SenderMessage)
	}
	if got.RecipientMessage.ID != 1 || got.RecipientMessage.Pts != 1 || got.RecipientMessage.Out || got.RecipientMessage.Peer.ID != sender.ID {
		t.Fatalf("recipient message = %+v, want first incoming box from sender", got.RecipientMessage)
	}
	if got.SenderMessage.UID == 0 || got.SenderMessage.UID != got.RecipientMessage.UID {
		t.Fatalf("uid = sender %d recipient %d, want shared private message uid", got.SenderMessage.UID, got.RecipientMessage.UID)
	}

	senderHistory, err := messages.ListByUser(ctx, sender.ID, domain.MessageFilter{HasPeer: true, Peer: got.SenderMessage.Peer, Limit: 10})
	if err != nil {
		t.Fatalf("sender history: %v", err)
	}
	recipientHistory, err := messages.ListByUser(ctx, recipient.ID, domain.MessageFilter{HasPeer: true, Peer: got.RecipientMessage.Peer, Limit: 10})
	if err != nil {
		t.Fatalf("recipient history: %v", err)
	}
	if len(senderHistory.Messages) != 1 || len(recipientHistory.Messages) != 1 {
		t.Fatalf("history sizes = sender %d recipient %d, want both owner partitions populated", len(senderHistory.Messages), len(recipientHistory.Messages))
	}
	if !sameMessageEntities(senderHistory.Messages[0].Entities, req.Entities) || !sameMessageEntities(recipientHistory.Messages[0].Entities, req.Entities) {
		t.Fatalf("history entities sender=%+v recipient=%+v, want %+v", senderHistory.Messages[0].Entities, recipientHistory.Messages[0].Entities, req.Entities)
	}

	events, err := NewUpdateEventStore(pool).ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("list recipient events: %v", err)
	}
	if len(events) != 1 || events[0].Message.ID != got.RecipientMessage.ID || len(events[0].Users) != 1 || events[0].Users[0].ID != sender.ID {
		t.Fatalf("recipient events = %+v, want new message with sender user", events)
	}

	var pendingOutbox int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM dispatch_outbox
		WHERE target_user_id = ANY($1::bigint[])
		  AND status = 'pending'
	`, []int64{sender.ID, recipient.ID}).Scan(&pendingOutbox); err != nil {
		t.Fatalf("count dispatch outbox: %v", err)
	}
	if pendingOutbox != 2 {
		t.Fatalf("pending outbox = %d, want sender + recipient dispatch rows", pendingOutbox)
	}
	var excludeAuthKeyID, excludeSessionID int64
	if err := pool.QueryRow(ctx, `
		SELECT exclude_auth_key_id, exclude_session_id
		FROM dispatch_outbox
		WHERE target_user_id = $1
	`, sender.ID).Scan(&excludeAuthKeyID, &excludeSessionID); err != nil {
		t.Fatalf("sender dispatch outbox: %v", err)
	}
	if excludeAuthKeyID != authKeyIDToInt64(originAuthKeyID) || excludeSessionID != 77 {
		t.Fatalf("sender dispatch exclude = auth %d session %d, want origin auth/session", excludeAuthKeyID, excludeSessionID)
	}

	dup, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("SendPrivateText duplicate: %v", err)
	}
	if !dup.Duplicate || dup.SenderMessage.ID != got.SenderMessage.ID || dup.RecipientMessage.ID != got.RecipientMessage.ID {
		t.Fatalf("duplicate = %+v, want original message boxes", dup)
	}
}

func TestMessageStoreWebViewDataServiceActionRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1666"+suffix+"31", "WebViewSender", "")
	recipient := createTestUser(t, ctx, users, "+1666"+suffix+"32", "WebViewRecipient", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	req := domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        9001,
		Date:            1700000210,
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind: domain.MessageServiceActionWebViewDataSent,
				WebViewData: &domain.MessageWebViewDataAction{
					ButtonText: "Open",
					Data:       `{"ok":true}`,
				},
			},
		},
	}
	got, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	assertWebViewData := func(name string, msg domain.Message) {
		t.Helper()
		if msg.Media == nil || msg.Media.ServiceAction == nil ||
			msg.Media.ServiceAction.Kind != domain.MessageServiceActionWebViewDataSent ||
			msg.Media.ServiceAction.WebViewData == nil {
			t.Fatalf("%s media = %+v, want webview data service action", name, msg.Media)
		}
		if data := msg.Media.ServiceAction.WebViewData; data.ButtonText != "Open" || data.Data != `{"ok":true}` {
			t.Fatalf("%s webview data = %+v, want original payload", name, data)
		}
	}
	assertWebViewData("sender", got.SenderMessage)
	assertWebViewData("recipient", got.RecipientMessage)

	dupReq := req
	dupReq.Date = 1700000211
	dupReq.Media = &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionWebViewDataSent,
			WebViewData: &domain.MessageWebViewDataAction{
				ButtonText: "Changed",
				Data:       `{"ok":false}`,
			},
		},
	}
	if _, err := messages.SendPrivateText(ctx, dupReq); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("changed-media duplicate err = %v, want ErrMessageRandomIDDuplicate", err)
	}

	recipientHistory, err := messages.ListByUser(ctx, recipient.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID},
		Limit:   10,
	})
	if err != nil || len(recipientHistory.Messages) != 1 {
		t.Fatalf("recipient history = %+v err=%v, want one message", recipientHistory, err)
	}
	assertWebViewData("recipient history", recipientHistory.Messages[0])

	events, err := NewUpdateEventStore(pool).ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("recipient events = %+v err=%v, want one event", events, err)
	}
	if events[0].Type != domain.UpdateEventNewMessage || events[0].Message.ID != got.RecipientMessage.ID {
		t.Fatalf("recipient event = %+v, want original new message event", events[0])
	}
	assertWebViewData("recipient event", events[0].Message)
}

func TestMessageStorePhoneCallServiceFirstMessageFeedsDialogsAndUpdates(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	caller := createTestUser(t, ctx, users, "+1666"+suffix+"41", "CallFirstSender", "")
	callee := createTestUser(t, ctx, users, "+1666"+suffix+"42", "CallFirstRecipient", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{caller.ID, callee.ID})
	})

	const callID int64 = 0x1020304050607080
	messages := NewMessageStore(pool)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    caller.ID,
		RecipientUserID: callee.ID,
		RandomID:        4041001,
		Date:            1700000410,
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind: domain.MessageServiceActionPhoneCall,
				Call: &domain.MessagePhoneCallAction{
					CallID: callID,
					Reason: string(domain.PhoneCallDiscardReasonMissed),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("SendPrivateText phone call service: %v", err)
	}
	if sent.SenderMessage.ID != 1 || sent.RecipientMessage.ID != 1 {
		t.Fatalf("first phone call boxes = sender %d recipient %d, want both first message", sent.SenderMessage.ID, sent.RecipientMessage.ID)
	}

	assertPhoneCallMessage := func(name string, msg domain.Message, ownerID, peerID, fromID int64, out bool) {
		t.Helper()
		if msg.OwnerUserID != ownerID || msg.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: peerID}) ||
			msg.From != (domain.Peer{Type: domain.PeerTypeUser, ID: fromID}) || msg.Out != out {
			t.Fatalf("%s identity = owner %d peer %+v from %+v out %v", name, msg.OwnerUserID, msg.Peer, msg.From, msg.Out)
		}
		if msg.Media == nil || msg.Media.Kind != domain.MessageMediaKindService ||
			msg.Media.ServiceAction == nil || msg.Media.ServiceAction.Kind != domain.MessageServiceActionPhoneCall ||
			msg.Media.ServiceAction.Call == nil {
			t.Fatalf("%s media = %+v, want phone_call service action", name, msg.Media)
		}
		call := msg.Media.ServiceAction.Call
		if call.CallID != callID || call.Reason != string(domain.PhoneCallDiscardReasonMissed) || call.Duration != 0 {
			t.Fatalf("%s phone call action = %+v", name, call)
		}
	}
	assertPhoneCallMessage("sender box", sent.SenderMessage, caller.ID, callee.ID, caller.ID, true)
	assertPhoneCallMessage("recipient box", sent.RecipientMessage, callee.ID, caller.ID, caller.ID, false)

	dialogs := NewDialogStore(pool)
	calleeDialogs, err := dialogs.ListByUser(ctx, callee.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("callee dialogs: %v", err)
	}
	if len(calleeDialogs.Dialogs) != 1 || calleeDialogs.Dialogs[0].TopMessage != sent.RecipientMessage.ID ||
		calleeDialogs.Dialogs[0].UnreadCount != 1 || calleeDialogs.Dialogs[0].ReadInboxMaxID != 0 {
		t.Fatalf("callee dialogs = %+v, want unread first call service as top", calleeDialogs.Dialogs)
	}
	if len(calleeDialogs.Messages) != 1 {
		t.Fatalf("callee dialog messages = %+v, want top message payload", calleeDialogs.Messages)
	}
	assertPhoneCallMessage("callee dialog top", calleeDialogs.Messages[0], callee.ID, caller.ID, caller.ID, false)
	if _, ok := findDialogUserByID(calleeDialogs.Users, caller.ID); !ok {
		t.Fatalf("callee dialog users = %+v, want caller snapshot", calleeDialogs.Users)
	}

	calleeHistory, err := messages.ListByUser(ctx, callee.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: caller.ID},
		Limit:   10,
	})
	if err != nil || len(calleeHistory.Messages) != 1 {
		t.Fatalf("callee history = %+v err=%v, want one first call service", calleeHistory, err)
	}
	assertPhoneCallMessage("callee history", calleeHistory.Messages[0], callee.ID, caller.ID, caller.ID, false)
	if _, ok := findDialogUserByID(calleeHistory.Users, caller.ID); !ok {
		t.Fatalf("callee history users = %+v, want caller snapshot", calleeHistory.Users)
	}

	updates := NewUpdateEventStore(pool)
	calleeEvents, err := updates.ListAfter(ctx, callee.ID, 0, 10)
	if err != nil || len(calleeEvents) != 1 {
		t.Fatalf("callee events = %+v err=%v, want one new_message", calleeEvents, err)
	}
	if calleeEvents[0].Type != domain.UpdateEventNewMessage || calleeEvents[0].Pts != sent.RecipientMessage.Pts {
		t.Fatalf("callee event = %+v, want new_message pts %d", calleeEvents[0], sent.RecipientMessage.Pts)
	}
	assertPhoneCallMessage("callee difference event", calleeEvents[0].Message, callee.ID, caller.ID, caller.ID, false)
	if _, ok := findDialogUserByID(calleeEvents[0].Users, caller.ID); !ok {
		t.Fatalf("callee event users = %+v, want caller snapshot", calleeEvents[0].Users)
	}

	batch, err := updates.BatchByCursor(ctx, []store.EventCursor{{UserID: callee.ID, Pts: calleeEvents[0].Pts}})
	if err != nil || len(batch) != 1 {
		t.Fatalf("callee batch events = %+v err=%v, want one dispatch event", batch, err)
	}
	assertPhoneCallMessage("callee dispatch event", batch[0].Message, callee.ID, caller.ID, caller.ID, false)
	if _, ok := findDialogUserByID(batch[0].Users, caller.ID); !ok {
		t.Fatalf("callee batch users = %+v, want caller snapshot", batch[0].Users)
	}
}

func TestUpdateEventStorePreservesChannelForwardRefsWithoutChannelSnapshot(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1666"+suffix+"21", "ForwardSender", "")
	recipient := createTestUser(t, ctx, users, "+1666"+suffix+"22", "ForwardRecipient", "")
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	created, err := NewChannelStore(pool).CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: sender.ID,
		Title:         "forward source " + suffix,
		Broadcast:     true,
		Date:          1700000250,
	})
	if err != nil {
		t.Fatalf("create source channel: %v", err)
	}
	source := created.Channel
	channelIDs = append(channelIDs, source.ID)

	sent, err := NewMessageStore(pool).SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        123466,
		Message:         "forwarded from channel",
		Forward: &domain.MessageForward{
			From: domain.Peer{Type: domain.PeerTypeChannel, ID: source.ID},
			Date: 1700000249,
		},
		Date: 1700000260,
	})
	if err != nil {
		t.Fatalf("SendPrivateText with channel forward: %v", err)
	}

	updates := NewUpdateEventStore(pool)
	events, err := updates.ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("list recipient events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("recipient events = %+v, want one new_message", events)
	}
	requireChannelForwardRefOnlyEvent(t, events[0], source.ID, sender.ID, sent.RecipientMessage.ID)

	batch, err := updates.BatchByCursor(ctx, []store.EventCursor{{UserID: recipient.ID, Pts: events[0].Pts}})
	if err != nil {
		t.Fatalf("batch dispatch event: %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("batch dispatch events = %+v, want one new_message", batch)
	}
	requireChannelForwardRefOnlyEvent(t, batch[0], source.ID, sender.ID, sent.RecipientMessage.ID)
}

func TestMessageStoreSendPrivateTextDuplicateDoesNotAppendPts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{
		AccessHash: 51,
		Phone:      "+1666" + suffix + "11",
		FirstName:  "DuplicateSender",
	})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 52,
		Phone:      "+1666" + suffix + "12",
		FirstName:  "DuplicateRecipient",
	})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	req := domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        123456,
		Message:         "first",
		Date:            1700000200,
	}
	got, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	if got.SenderMessage.Pts != 1 || got.RecipientMessage.Pts != 1 {
		t.Fatalf("first send = %+v/%+v, want pts=1", got.SenderMessage, got.RecipientMessage)
	}
	dup, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("SendPrivateText duplicate: %v", err)
	}
	if !dup.Duplicate || dup.SenderMessage.ID != got.SenderMessage.ID || dup.RecipientMessage.ID != got.RecipientMessage.ID {
		t.Fatalf("duplicate = %+v, want original message boxes", dup)
	}

	updateEvents := NewUpdateEventStore(pool)
	senderEvents, err := updateEvents.ListAfter(ctx, sender.ID, 0, 10)
	if err != nil {
		t.Fatalf("list sender events after duplicate: %v", err)
	}
	recipientEvents, err := updateEvents.ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("list recipient events after duplicate: %v", err)
	}
	if len(senderEvents) != 1 || senderEvents[0].Type != domain.UpdateEventNewMessage || senderEvents[0].Pts != 1 {
		t.Fatalf("sender events after duplicate = %+v, want only original new_message pts=1", senderEvents)
	}
	if len(recipientEvents) != 1 || recipientEvents[0].Type != domain.UpdateEventNewMessage || recipientEvents[0].Pts != 1 {
		t.Fatalf("recipient events after duplicate = %+v, want only original new_message pts=1", recipientEvents)
	}

	nextReq := req
	nextReq.RandomID = 123457
	nextReq.Message = "after duplicate"
	nextReq.Date = 1700000201
	next, err := messages.SendPrivateText(ctx, nextReq)
	if err != nil {
		t.Fatalf("SendPrivateText after duplicate: %v", err)
	}
	if next.SenderMessage.Pts != 2 || next.RecipientMessage.Pts != 2 {
		t.Fatalf("next send = %+v/%+v, want pts=2 without duplicate event", next.SenderMessage, next.RecipientMessage)
	}
}

func requireChannelForwardRefOnlyEvent(t *testing.T, event domain.UpdateEvent, sourceChannelID, senderID int64, messageID int) {
	t.Helper()
	if event.Type != domain.UpdateEventNewMessage || event.Message.ID != messageID {
		t.Fatalf("event = %+v, want new_message for message %d", event, messageID)
	}
	if event.Message.Forward == nil {
		t.Fatalf("event message forward = nil, want channel %d", sourceChannelID)
	}
	if event.Message.Forward.From != (domain.Peer{Type: domain.PeerTypeChannel, ID: sourceChannelID}) {
		t.Fatalf("event message forward from = %+v, want channel %d", event.Message.Forward.From, sourceChannelID)
	}
	if len(event.Channels) != 0 {
		t.Fatalf("event channels = %+v, want no store-level channel snapshot", event.Channels)
	}
	if _, ok := findDialogUserByID(event.Users, senderID); !ok {
		t.Fatalf("event users = %+v, want sender %d", event.Users, senderID)
	}
}

func TestMessageStoreSendPrivateTextRecomputesInboxUnreadFromReadMax(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{
		AccessHash: 41,
		Phone:      "+1667" + suffix + "01",
		FirstName:  "Sender",
	})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 42,
		Phone:      "+1667" + suffix + "02",
		FirstName:  "Recipient",
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
		RandomID:        823001,
		Message:         "already read",
		Date:            1700000400,
	})
	if err != nil {
		t.Fatalf("SendPrivateText first: %v", err)
	}
	if _, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: recipient.ID,
		Peer:        peer,
		MaxID:       first.RecipientMessage.ID,
		Date:        1700000410,
	}); err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE dialogs
		SET unread_count = 2
		WHERE user_id = $1
		  AND peer_type = $2
		  AND peer_id = $3
	`, recipient.ID, string(domain.PeerTypeUser), sender.ID); err != nil {
		t.Fatalf("corrupt unread count: %v", err)
	}

	bodies := []string{"one", "two", "three"}
	var last domain.SendPrivateTextResult
	for i, body := range bodies {
		last, err = messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    sender.ID,
			RecipientUserID: recipient.ID,
			RandomID:        823010 + int64(i),
			Message:         body,
			Date:            1700000420 + i,
		})
		if err != nil {
			t.Fatalf("SendPrivateText %q: %v", body, err)
		}
	}

	var unreadCount, topMessageID, readInboxMaxID int
	var topBody string
	if err := pool.QueryRow(ctx, `
		SELECT d.unread_count, d.top_message_id, d.read_inbox_max_id, COALESCE(m.body, '')
		FROM dialogs d
		LEFT JOIN message_boxes m
		  ON m.owner_user_id = d.user_id
		 AND m.box_id = d.top_message_id
		 AND NOT m.deleted
		WHERE d.user_id = $1
		  AND d.peer_type = $2
		  AND d.peer_id = $3
	`, recipient.ID, string(domain.PeerTypeUser), sender.ID).Scan(&unreadCount, &topMessageID, &readInboxMaxID, &topBody); err != nil {
		t.Fatalf("load dialog: %v", err)
	}
	if unreadCount != len(bodies) || topMessageID != last.RecipientMessage.ID || readInboxMaxID != first.RecipientMessage.ID || topBody != "three" {
		t.Fatalf("dialog unread/top = count %d top %d read %d body %q, want count=3 top=%d read=%d body=three",
			unreadCount, topMessageID, readInboxMaxID, topBody, last.RecipientMessage.ID, first.RecipientMessage.ID)
	}
}

func TestMessageStoreSendPrivateTextRollbackDoesNotAdvancePts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{
		AccessHash: 31,
		Phone:      "+1777" + suffix + "01",
		FirstName:  "GapSender",
	})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 32,
		Phone:      "+1777" + suffix + "02",
		FirstName:  "GapRecipient",
	})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        223344,
		Message:         "seed box",
		Date:            1700000210,
	}); err != nil {
		t.Fatalf("seed SendPrivateText: %v", err)
	}

	failing := NewMessageStore(pool, WithMessageAllocators(fixedBoxIDAllocator{next: 1}))
	_, err = failing.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        223345,
		Message:         "should roll back",
		Date:            1700000211,
	})
	if err == nil {
		t.Fatal("SendPrivateText succeeded, want box id conflict")
	}

	updateEvents := NewUpdateEventStore(pool)
	events, err := updateEvents.ListAfter(ctx, sender.ID, 1, 10)
	if err != nil {
		t.Fatalf("list sender events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("sender events after rollback = %+v, want none", events)
	}
	senderPts, err := updateEvents.MaxContiguousPts(ctx, sender.ID)
	if err != nil {
		t.Fatalf("sender MaxContiguousPts: %v", err)
	}
	recipientPts, err := updateEvents.MaxContiguousPts(ctx, recipient.ID)
	if err != nil {
		t.Fatalf("recipient MaxContiguousPts: %v", err)
	}
	if senderPts != 1 || recipientPts != 1 {
		t.Fatalf("pts after rollback sender=%d recipient=%d, want both unchanged at 1", senderPts, recipientPts)
	}
}

func TestMessageStoreConcurrentRandomIDIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{
		AccessHash: 41,
		Phone:      "+1888" + suffix + "01",
		FirstName:  "ConcurrentSender",
	})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 42,
		Phone:      "+1888" + suffix + "02",
		FirstName:  "ConcurrentRecipient",
	})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	boxCounters := &perUserCounterAllocator{}
	messages := NewMessageStore(pool, WithMessageAllocators(boxCounters))
	req := domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        556677,
		Message:         "same random id",
		Date:            1700000220,
	}

	const workers = 8
	results := make(chan domain.SendPrivateTextResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := messages.SendPrivateText(ctx, req)
			if err != nil {
				errs <- err
				return
			}
			results <- res
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("SendPrivateText: %v", err)
	}
	var uid int64
	duplicates := 0
	successes := 0
	for res := range results {
		if res.SenderMessage.UID == 0 || res.RecipientMessage.UID == 0 {
			t.Fatalf("result = %+v, want populated shared message uid", res)
		}
		if uid == 0 {
			uid = res.SenderMessage.UID
		}
		if res.SenderMessage.UID != uid || res.RecipientMessage.UID != uid {
			t.Fatalf("result = %+v, want same private message uid %d", res, uid)
		}
		if res.Duplicate {
			duplicates++
		} else {
			successes++
		}
	}
	if successes != 1 || duplicates != workers-1 {
		t.Fatalf("successes=%d duplicates=%d, want one insert and duplicate rest", successes, duplicates)
	}

	var privateCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM private_messages
		WHERE sender_user_id = $1
		  AND random_id = $2
	`, sender.ID, req.RandomID).Scan(&privateCount); err != nil {
		t.Fatalf("count private_messages: %v", err)
	}
	if privateCount != 1 {
		t.Fatalf("private message count = %d, want 1", privateCount)
	}
	var boxCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM message_boxes
		WHERE private_message_id = $1
	`, uid).Scan(&boxCount); err != nil {
		t.Fatalf("count message boxes: %v", err)
	}
	if boxCount != 2 {
		t.Fatalf("message box count = %d, want sender + recipient boxes", boxCount)
	}
	updateEvents := NewUpdateEventStore(pool)
	senderEvents, err := updateEvents.ListAfter(ctx, sender.ID, 0, workers+2)
	if err != nil {
		t.Fatalf("list sender events: %v", err)
	}
	recipientEvents, err := updateEvents.ListAfter(ctx, recipient.ID, 0, workers+2)
	if err != nil {
		t.Fatalf("list recipient events: %v", err)
	}
	if len(senderEvents) != 1 || senderEvents[0].Type != domain.UpdateEventNewMessage || senderEvents[0].Pts != 1 {
		t.Fatalf("sender events = %+v, want one new_message and no extra noop", senderEvents)
	}
	if len(recipientEvents) != 1 || recipientEvents[0].Type != domain.UpdateEventNewMessage || recipientEvents[0].Pts != 1 {
		t.Fatalf("recipient events = %+v, want one new_message and no extra noop", recipientEvents)
	}
}
