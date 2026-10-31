package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestSendPrivateMediaSurvivesUpdateEvent verifies a private message with media:
//   - after sending, message_boxes persists the media snapshot;
//   - the recipient gets the media back via UpdateEventStore.ListAfter (the rebuild path shared by
//     online outbox / offline getDifference) (the update event query once omitted m.media, losing
//     media for the recipient/offline; this test guards that fix);
//   - history (ListByUser) also reads back with media.
func TestSendPrivateMediaSurvivesUpdateEvent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{AccessHash: 61, Phone: "+1998" + suffix + "01", FirstName: "MediaSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 62, Phone: "+1998" + suffix + "02", FirstName: "MediaRecipient"})
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

	media := &domain.MessageMedia{
		Kind: domain.MessageMediaKindDocument,
		Document: &domain.Document{
			ID:         9200000000000000001,
			AccessHash: 9,
			DCID:       2,
			MimeType:   "application/x-tgsticker",
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker, Alt: "\U0001f600", StickerSetID: 5, StickerSetAccessHash: 7}},
		},
	}
	res, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        time.Now().UnixNano(),
		Message:         "", // media only (no caption)
		Media:           media,
		Date:            int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("send private media: %v", err)
	}

	// The send result carries media on both ends.
	for name, msg := range map[string]domain.Message{"sender": res.SenderMessage, "recipient": res.RecipientMessage} {
		if msg.Media == nil || msg.Media.Kind != domain.MessageMediaKindDocument || msg.Media.Document == nil || msg.Media.Document.ID != media.Document.ID {
			t.Fatalf("%s message media lost: %+v", name, msg.Media)
		}
	}

	// Key: the recipient still gets media via the update-event rebuild (the path shared by online push / offline difference).
	events := NewUpdateEventStore(pool)
	got, err := events.ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("list recipient events: %v", err)
	}
	var found bool
	for _, ev := range got {
		if ev.Type == domain.UpdateEventNewMessage && ev.Message.ID != 0 {
			found = true
			if ev.Message.Media == nil || ev.Message.Media.Document == nil || ev.Message.Media.Document.ID != media.Document.ID {
				t.Fatalf("recipient update-event message lost media: %+v", ev.Message.Media)
			}
		}
	}
	if !found {
		t.Fatal("no new_message event for recipient")
	}

	// history also reads back with media.
	list, err := messages.ListByUser(ctx, recipient.ID, domain.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list by user: %v", err)
	}
	if len(list.Messages) == 0 || list.Messages[0].Media == nil || list.Messages[0].Media.Document == nil {
		t.Fatalf("history message lost media: %+v", list.Messages)
	}
}

func TestEditPrivateTodoMediaByParticipantPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{AccessHash: 65, Phone: "+1998" + suffix + "05", FirstName: "TodoSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 66, Phone: "+1998" + suffix + "06", FirstName: "TodoRecipient"})
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
	base := &domain.MessageMedia{
		Kind: domain.MessageMediaKindTodo,
		Todo: &domain.MessageTodo{
			Title:             "shared todo",
			OthersCanComplete: true,
			Items:             []domain.MessageTodoItem{{ID: 1, Title: "one"}},
		},
	}
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        time.Now().UnixNano(),
		Message:         "",
		Media:           base,
		Date:            int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("send private todo: %v", err)
	}

	updated := &domain.MessageMedia{
		Kind: domain.MessageMediaKindTodo,
		Todo: &domain.MessageTodo{
			Title:             base.Todo.Title,
			OthersCanComplete: true,
			Items:             append([]domain.MessageTodoItem(nil), base.Todo.Items...),
			Completions:       []domain.MessageTodoCompletion{{ID: 1, CompletedBy: recipient.ID, Date: 1700000700}},
		},
	}
	edited, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID:                  recipient.ID,
		Peer:                         domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID},
		ID:                           sent.RecipientMessage.ID,
		Message:                      "",
		Media:                        updated,
		EditDate:                     1700000700,
		AllowTodoParticipantMutation: true,
	})
	if err != nil {
		t.Fatalf("participant edit todo media: %v", err)
	}
	if len(edited.Edited) != 2 {
		t.Fatalf("edited boxes = %d, want sender and recipient", len(edited.Edited))
	}

	senderView, err := messages.GetByIDs(ctx, sender.ID, []int{sent.SenderMessage.ID})
	if err != nil {
		t.Fatalf("sender get by ids: %v", err)
	}
	if len(senderView.Messages) != 1 || senderView.Messages[0].Media == nil || senderView.Messages[0].Media.Todo == nil {
		t.Fatalf("sender todo media = %+v, want todo", senderView.Messages)
	}
	got := senderView.Messages[0].Media.Todo.Completions
	if len(got) != 1 || got[0].ID != 1 || got[0].CompletedBy != recipient.ID {
		t.Fatalf("sender completions = %+v, want recipient completed item", got)
	}
}

func TestEditChannelTodoMediaByParticipantPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 67, Phone: "+1998" + suffix + "07", FirstName: "TodoChannelOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 68, Phone: "+1998" + suffix + "08", FirstName: "TodoChannelMember"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Todo Channel " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000710,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	base := &domain.MessageMedia{
		Kind: domain.MessageMediaKindTodo,
		Todo: &domain.MessageTodo{
			Title:             "group todo",
			OthersCanAppend:   true,
			OthersCanComplete: true,
			Items:             []domain.MessageTodoItem{{ID: 1, Title: "one"}, {ID: 2, Title: "two"}},
		},
	}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  time.Now().UnixNano(),
		Media:     base,
		Date:      1700000711,
	})
	if err != nil {
		t.Fatalf("send channel todo: %v", err)
	}

	updated := &domain.MessageMedia{
		Kind: domain.MessageMediaKindTodo,
		Todo: &domain.MessageTodo{
			Title:             base.Todo.Title,
			OthersCanAppend:   true,
			OthersCanComplete: true,
			Items:             append([]domain.MessageTodoItem(nil), base.Todo.Items...),
			Completions:       []domain.MessageTodoCompletion{{ID: 1, CompletedBy: member.ID, Date: 1700000712}},
		},
	}
	edited, err := channels.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID:                       member.ID,
		ChannelID:                    channelID,
		ID:                           sent.Message.ID,
		Media:                        updated,
		AllowTodoParticipantMutation: true,
		TodoServiceAction: &domain.ChannelMessageAction{
			Type:      domain.ChannelActionTodoCompletions,
			Completed: []int{1},
		},
		EditDate: 1700000712,
	})
	if err != nil {
		t.Fatalf("participant edit channel todo media: %v", err)
	}
	if edited.Event.Pts != sent.Event.Pts+1 || edited.ServiceEvent.Pts != edited.Event.Pts+1 {
		t.Fatalf("todo channel pts edit=%d service=%d after send=%d, want two contiguous pts", edited.Event.Pts, edited.ServiceEvent.Pts, sent.Event.Pts)
	}
	if edited.ServiceMessage.Action == nil || edited.ServiceMessage.Action.Type != domain.ChannelActionTodoCompletions {
		t.Fatalf("service action = %+v, want todo completions", edited.ServiceMessage.Action)
	}
	if edited.ServiceMessage.ReplyTo == nil || edited.ServiceMessage.ReplyTo.MessageID != sent.Message.ID {
		t.Fatalf("service reply = %+v, want original message %d", edited.ServiceMessage.ReplyTo, sent.Message.ID)
	}

	history, err := channels.GetChannelMessages(ctx, member.ID, channelID, []int{sent.Message.ID, edited.ServiceMessage.ID})
	if err != nil {
		t.Fatalf("get channel todo messages: %v", err)
	}
	if len(history.Messages) != 2 {
		t.Fatalf("history messages = %d, want original + service", len(history.Messages))
	}
	var foundOriginal, foundService bool
	for _, msg := range history.Messages {
		switch msg.ID {
		case sent.Message.ID:
			foundOriginal = true
			if msg.Media == nil || msg.Media.Todo == nil || len(msg.Media.Todo.Completions) != 1 || msg.Media.Todo.Completions[0].CompletedBy != member.ID {
				t.Fatalf("stored todo media = %+v, want member completion", msg.Media)
			}
		case edited.ServiceMessage.ID:
			foundService = true
			if msg.Action == nil || msg.Action.Type != domain.ChannelActionTodoCompletions {
				t.Fatalf("stored service action = %+v, want todo completions", msg.Action)
			}
		}
	}
	if !foundOriginal || !foundService {
		t.Fatalf("history = %+v, missing original=%v service=%v", history.Messages, foundOriginal, foundService)
	}

	diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		Pts:       sent.Event.Pts,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list channel difference: %v", err)
	}
	if diff.Pts != edited.ServiceEvent.Pts {
		t.Fatalf("diff pts = %d, want service pts %d", diff.Pts, edited.ServiceEvent.Pts)
	}
	if len(diff.OtherUpdates) != 1 || diff.OtherUpdates[0].Type != domain.ChannelUpdateEditMessage || diff.OtherUpdates[0].Pts != edited.Event.Pts {
		t.Fatalf("diff other updates = %+v, want edit event", diff.OtherUpdates)
	}
	if len(diff.NewMessages) != 1 || diff.NewMessages[0].ID != edited.ServiceMessage.ID || diff.NewMessages[0].Action == nil {
		t.Fatalf("diff new messages = %+v, want todo service message", diff.NewMessages)
	}
}

func TestSendChannelMediaSurvivesDifference(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 63, Phone: "+1998" + suffix + "03", FirstName: "MediaChannelOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 64, Phone: "+1998" + suffix + "04", FirstName: "MediaChannelMember"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Media Difference " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	media := &domain.MessageMedia{
		Kind: domain.MessageMediaKindDocument,
		Document: &domain.Document{
			ID:         9200000000000000002,
			AccessHash: 10,
			DCID:       2,
			MimeType:   "application/octet-stream",
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "telesrv-media.bin"}},
		},
	}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  time.Now().UnixNano(),
		Message:   "",
		Media:     media,
		Date:      int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("send channel media: %v", err)
	}
	if sent.Message.Media == nil || sent.Message.Media.Document == nil || sent.Message.Media.Document.ID != media.Document.ID {
		t.Fatalf("send result lost media: %+v", sent.Message.Media)
	}

	diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		Pts:       created.Event.Pts,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list channel difference: %v", err)
	}
	var found bool
	for _, msg := range diff.NewMessages {
		if msg.ID == sent.Message.ID {
			found = true
			if msg.Media == nil || msg.Media.Document == nil || msg.Media.Document.ID != media.Document.ID {
				t.Fatalf("channel difference message lost media: %+v", msg.Media)
			}
		}
	}
	if !found {
		t.Fatalf("sent media message %d not found in channel difference: %+v", sent.Message.ID, diff.NewMessages)
	}
}
