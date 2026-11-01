package rpc

import (
	"context"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strings"
	appchannels "telesrv/internal/app/channels"
	appupdates "telesrv/internal/app/updates"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestChannelRealtimeRecipientsPreferOnlineMembers(t *testing.T) {
	ctx := context.Background()
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Fanout",
		MemberUserIDs: []int64{1002, 1999},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sessions := &captureSessions{
		onlineUserIDs:  []int64{1999, 3000},
		channelViewers: map[int64][]int64{created.Channel.ID: {1999}},
		channelMembers: map[int64][]int64{created.Channel.ID: {1999, 3000}},
	}
	r := New(Config{}, Deps{
		Channels: channelService,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	contains := func(items []int64, want int64) bool {
		for _, item := range items {
			if item == want {
				return true
			}
		}
		return false
	}

	got := r.channelTransientRecipients(ctx, channelTransientMembers, created.Channel.ID, []int64{1002})
	if !contains(got, 1999) {
		t.Fatalf("recipients = %v, want online active member 1999", got)
	}
	if contains(got, 3000) {
		t.Fatalf("recipients = %v, non-member online user leaked", got)
	}
	if !contains(got, 1002) {
		t.Fatalf("recipients = %v, want explicitly affected recipient 1002", got)
	}
	onlines, err := r.onMessagesGetOnlines(WithUserID(ctx, 1001), &tg.InputPeerChannel{
		ChannelID:  created.Channel.ID,
		AccessHash: created.Channel.AccessHash,
	})
	if err != nil {
		t.Fatalf("messages.getOnlines: %v", err)
	}
	if onlines.Onlines != 2 {
		t.Fatalf("messages.getOnlines = %d, want caller plus online active member", onlines.Onlines)
	}
}

func TestChannelSendHistoryAndDifferenceRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550002001", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550002002", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	deliveryOutbox := memory.NewDeliveryOutboxStore()
	channelStore.AttachDeliveryOutbox(deliveryOutbox)
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{
		Users:          appusers.NewService(userStore),
		Channels:       appchannels.NewService(channelStore),
		Sessions:       sessions,
		DeliveryOutbox: deliveryOutbox,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "RPC Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	sessions.clearMessages()

	var authKeyID [8]byte
	authKeyID[0] = 9
	sendCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), authKeyID), 77)
	sent, err := r.onMessagesSendMessage(sendCtx, &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "hello channel",
		RandomID: 99,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	sendUpdates := sent.(*tg.Updates)
	if id, ok := sendUpdates.Updates[0].(*tg.UpdateMessageID); !ok || id.ID != 3 || id.RandomID != 99 {
		t.Fatalf("message id update = %#v, want id=3 random_id=99", sendUpdates.Updates[0])
	}
	newMsg, ok := sendUpdates.Updates[1].(*tg.UpdateNewChannelMessage)
	if !ok || newMsg.Pts != 4 || newMsg.PtsCount != 1 {
		t.Fatalf("new channel update = %#v, want pts=4", sendUpdates.Updates[1])
	}
	msg := newMsg.Message.(*tg.Message)
	if msg.PeerID.(*tg.PeerChannel).ChannelID != channel.ID || msg.Message != "hello channel" || !msg.Out {
		t.Fatalf("channel message = %#v, want outgoing channel text", msg)
	}
	if pushed := sessions.snapshot(); pushed.message != nil {
		t.Fatalf("Core pushed durable channel update directly: %T", pushed.message)
	}

	history, err := r.onChannelsGetMessages(WithUserID(ctx, friend.ID), &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msg.ID}},
	})
	if err != nil {
		t.Fatalf("get channel messages: %v", err)
	}
	messages := history.(*tg.MessagesMessages)
	got := messages.Messages[0].(*tg.Message)
	if got.Message != "hello channel" || got.Out {
		t.Fatalf("history message = %#v, want incoming text for friend", got)
	}

	contentAuthKeyID := [8]byte{0x44}
	contentCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, friend.ID), contentAuthKeyID), 88)
	if ok, err := r.onChannelsReadMessageContents(contentCtx, &tg.ChannelsReadMessageContentsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      []int{msg.ID},
	}); err != nil || !ok {
		t.Fatalf("channels.readMessageContents = ok %v err %v, want true", ok, err)
	}
	contentItems := deliveryOutbox.Snapshot()
	if len(contentItems) != 1 {
		t.Fatalf("content-read durable deliveries = %+v, want one", contentItems)
	}
	contentItem := contentItems[0]
	if contentItem.TargetUserID != friend.ID || contentItem.ExcludeAuthKeyID != contentAuthKeyID || contentItem.ExcludeSessionID != 88 {
		t.Fatalf("content-read durable target/exclusion = %d/%x/%d, want friend/%x/88", contentItem.TargetUserID, contentItem.ExcludeAuthKeyID, contentItem.ExcludeSessionID, contentAuthKeyID)
	}
	contentUpdates := requireDeliveryUpdates(t, contentItem)
	if len(contentUpdates.Updates) != 1 {
		t.Fatalf("content-read durable updates = %+v, want one update", contentUpdates.Updates)
	}
	contentRead, ok := contentUpdates.Updates[0].(*tg.UpdateChannelReadMessagesContents)
	if !ok || contentRead.ChannelID != channel.ID || len(contentRead.Messages) != 1 || contentRead.Messages[0] != msg.ID {
		t.Fatalf("content-read update = %#v, want channel %d msg %d", contentUpdates.Updates[0], channel.ID, msg.ID)
	}

	diff, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     newMsg.Pts - 1,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("channel difference: %v", err)
	}
	fullDiff, ok := diff.(*tg.UpdatesChannelDifference)
	if !ok || fullDiff.Pts != newMsg.Pts || len(fullDiff.NewMessages) != 1 {
		t.Fatalf("diff = %T %+v, want one new message at pts=%d", diff, diff, newMsg.Pts)
	}
	if _, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     fullDiff.Pts + 1,
		Limit:   10,
	}); err == nil || !strings.Contains(err.Error(), "PERSISTENT_TIMESTAMP_INVALID") {
		t.Fatalf("future channel pts err = %v, want PERSISTENT_TIMESTAMP_INVALID", err)
	}

	readOK, err := r.onChannelsReadHistory(WithUserID(ctx, friend.ID), &tg.ChannelsReadHistoryRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MaxID:   msg.ID,
	})
	if err != nil || !readOK {
		t.Fatalf("channels.readHistory = %v err %v, want true", readOK, err)
	}
	readItems := deliveryOutbox.Snapshot()
	if len(readItems) != 3 {
		t.Fatalf("durable deliveries after channel read = %+v, want prior content plus reader inbox and sender outbox", readItems)
	}
	ownerReadItem := requireDeliveryItemForUser(t, readItems, owner.ID)
	readPushUpdates := requireDeliveryUpdates(t, ownerReadItem)
	if len(readPushUpdates.Updates) != 1 {
		t.Fatalf("read outbox durable updates = %+v, want one update", readPushUpdates.Updates)
	}
	readOutbox, ok := readPushUpdates.Updates[0].(*tg.UpdateReadChannelOutbox)
	if !ok || readOutbox.ChannelID != channel.ID || readOutbox.MaxID != msg.ID {
		t.Fatalf("read outbox update = %#v, want channel %d max %d", readPushUpdates.Updates[0], channel.ID, msg.ID)
	}
	fullAfterRead, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel after read: %v", err)
	}
	fullChannel := fullAfterRead.FullChat.(*tg.ChannelFull)
	if fullChannel.ReadOutboxMaxID != msg.ID {
		t.Fatalf("full channel read_outbox = %d, want %d", fullChannel.ReadOutboxMaxID, msg.ID)
	}
	readers, err := r.onMessagesGetMessageReadParticipants(WithUserID(ctx, owner.ID), &tg.MessagesGetMessageReadParticipantsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: msg.ID,
	})
	if err != nil {
		t.Fatalf("get message read participants: %v", err)
	}
	if len(readers) != 1 || readers[0].UserID != friend.ID || readers[0].Date == 0 {
		t.Fatalf("read participants = %+v, want friend read date", readers)
	}

	editReq := &tg.MessagesEditMessageRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      msg.ID,
		Message: "edited channel",
	}
	editReq.SetMessage("edited channel")
	edited, err := r.onMessagesEditMessage(WithUserID(ctx, owner.ID), editReq)
	if err != nil {
		t.Fatalf("edit channel message: %v", err)
	}
	editUpdates := edited.(*tg.Updates)
	edit, ok := editUpdates.Updates[0].(*tg.UpdateEditChannelMessage)
	if !ok || edit.Pts != newMsg.Pts+1 || edit.PtsCount != 1 {
		t.Fatalf("edit update = %#v, want updateEditChannelMessage pts=%d", editUpdates.Updates[0], newMsg.Pts+1)
	}
	if edit.Message.(*tg.Message).Message != "edited channel" {
		t.Fatalf("edited message = %#v, want edited text", edit.Message)
	}
	editData, err := r.onMessagesGetMessageEditData(WithUserID(ctx, owner.ID), &tg.MessagesGetMessageEditDataRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   msg.ID,
	})
	if err != nil {
		t.Fatalf("get channel edit data: %v", err)
	}
	if editData.GetCaption() {
		t.Fatalf("channel edit data caption = true, want false for text-only message")
	}

	forwardReplyTo := &tg.InputReplyToMessage{ReplyToMsgID: msg.ID}
	forwardReplyTo.SetQuoteText("channel")
	forwardReq := &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ToPeer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:       []int{msg.ID},
		RandomID: []int64{100},
	}
	forwardReq.SetReplyTo(forwardReplyTo)
	forwarded, err := r.onMessagesForwardMessages(WithUserID(ctx, friend.ID), forwardReq)
	if err != nil {
		t.Fatalf("forward channel message: %v", err)
	}
	forwardUpdates := forwarded.(*tg.Updates)
	if id, ok := forwardUpdates.Updates[0].(*tg.UpdateMessageID); !ok || id.ID != msg.ID+1 || id.RandomID != 100 {
		t.Fatalf("forward id update = %#v, want id=%d", forwardUpdates.Updates[0], msg.ID+1)
	}
	forwardNew, ok := forwardUpdates.Updates[1].(*tg.UpdateNewChannelMessage)
	if !ok || forwardNew.Pts != edit.Pts+1 || forwardNew.PtsCount != 1 {
		t.Fatalf("forward new update = %#v, want pts=%d", forwardUpdates.Updates[1], edit.Pts+1)
	}
	forwardMsg := forwardNew.Message.(*tg.Message)
	if forwardMsg.Message != "edited channel" || forwardMsg.FwdFrom.FromID == nil {
		t.Fatalf("forward message = %#v, want fwd header and edited body", forwardMsg)
	}
	if header, ok := forwardMsg.ReplyTo.(*tg.MessageReplyHeader); !ok || header.ReplyToMsgID != msg.ID {
		t.Fatalf("forward reply header = %#v, want reply to channel message %d", forwardMsg.ReplyTo, msg.ID)
	}

	deleted, err := r.onChannelsDeleteMessages(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      []int{msg.ID, forwardMsg.ID},
	})
	if err != nil {
		t.Fatalf("delete channel messages: %v", err)
	}
	if deleted.Pts != forwardNew.Pts+2 || deleted.PtsCount != 2 {
		t.Fatalf("delete affected = %+v, want pts=%d count=2", deleted, forwardNew.Pts+2)
	}

	diff, err = r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     newMsg.Pts,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("channel difference after edit/delete: %v", err)
	}
	fullDiff, ok = diff.(*tg.UpdatesChannelDifference)
	if !ok || fullDiff.Pts != deleted.Pts || len(fullDiff.NewMessages) != 1 || len(fullDiff.OtherUpdates) != 3 {
		t.Fatalf("diff after edit/delete = %T %+v, want forward message id mapping plus edit/delete updates", diff, diff)
	}
	// The first other update in the difference is the updateMessageID of the
	// requester's own message: after a reconnect, reconcile the local pending via the
	// difference, avoiding a duplicate bubble.
	if mapping, ok := fullDiff.OtherUpdates[0].(*tg.UpdateMessageID); !ok || mapping.RandomID == 0 || mapping.ID == 0 {
		t.Fatalf("diff other[0] = %#v, want updateMessageID for own forwarded message", fullDiff.OtherUpdates[0])
	}
}

func TestChannelsReadMessageContentsAtomicallyClearsAndQueuesUpdates(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550002141", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550002142", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	deliveryOutbox := memory.NewDeliveryOutboxStore()
	channelStore.AttachDeliveryOutbox(deliveryOutbox)
	r := New(Config{}, Deps{
		Users:          appusers.NewService(userStore),
		Channels:       appchannels.NewService(channelStore),
		DeliveryOutbox: deliveryOutbox,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Reaction Read",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "owner message",
		RandomID: 21041,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	msgID := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message).ID
	req := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:    msgID,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f525"}},
	}
	req.SetReaction(req.Reaction)
	if _, err := r.onMessagesSendReaction(WithUserID(ctx, friend.ID), req); err != nil {
		t.Fatalf("friend send reaction: %v", err)
	}
	unread, err := r.onMessagesGetUnreadReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetUnreadReactionsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get unread reactions: %v", err)
	}
	unreadMessages, _, _ := searchMessagesPayload(t, unread)
	if len(unreadMessages) != 1 {
		t.Fatalf("unread reactions = %+v, want one message", unread)
	}

	contentAuthKeyID := [8]byte{0x66}
	contentCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), contentAuthKeyID), 99)
	if ok, err := r.onChannelsReadMessageContents(contentCtx, &tg.ChannelsReadMessageContentsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      []int{msgID},
	}); err != nil || !ok {
		t.Fatalf("channels.readMessageContents = ok %v err %v, want true", ok, err)
	}
	items := deliveryOutbox.Snapshot()
	if len(items) != 1 {
		t.Fatalf("reaction read durable deliveries = %+v, want one", items)
	}
	item := items[0]
	if item.TargetUserID != owner.ID || item.ExcludeAuthKeyID != contentAuthKeyID || item.ExcludeSessionID != 99 {
		t.Fatalf("reaction read durable target/exclusion = %d/%x/%d, want owner/%x/99", item.TargetUserID, item.ExcludeAuthKeyID, item.ExcludeSessionID, contentAuthKeyID)
	}
	pushedUpdates := requireDeliveryUpdates(t, item)
	if len(pushedUpdates.Updates) != 2 {
		t.Fatalf("reaction read durable updates = %+v, want content-read and reaction refresh", pushedUpdates.Updates)
	}
	var reactionUpdate *tg.UpdateMessageReactions
	for _, update := range pushedUpdates.Updates {
		if candidate, ok := update.(*tg.UpdateMessageReactions); ok {
			reactionUpdate = candidate
		}
	}
	if reactionUpdate == nil || reactionUpdate.MsgID != msgID {
		t.Fatalf("reaction read updates = %+v, want updateMessageReactions for %d", pushedUpdates.Updates, msgID)
	}
	for _, recent := range reactionUpdate.Reactions.RecentReactions {
		if recent.Unread {
			t.Fatalf("reaction read update recent = %+v, want unread cleared", reactionUpdate.Reactions.RecentReactions)
		}
	}
	unreadAfter, err := r.onMessagesGetUnreadReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetUnreadReactionsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get unread reactions after read contents: %v", err)
	}
	unreadAfterMessages, _, _ := searchMessagesPayload(t, unreadAfter)
	if len(unreadAfterMessages) != 0 {
		t.Fatalf("unread reactions after read contents = %+v, want empty", unreadAfter)
	}
}

func TestChannelReadHistoryDurablyQueuesReadInboxWithoutAccountPTS(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550002131", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550002132", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	deliveryOutbox := memory.NewDeliveryOutboxStore()
	channelStore.AttachDeliveryOutbox(deliveryOutbox)
	updates := appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore())
	r := New(Config{}, Deps{
		Users:          appusers.NewService(userStore),
		Channels:       appchannels.NewService(channelStore),
		Updates:        updates,
		DeliveryOutbox: deliveryOutbox,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Read Channel Inbox",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "read me",
		RandomID: 301,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	newUpdate := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage)
	msg := newUpdate.Message.(*tg.Message)
	friendCtx := WithUserID(ctx, friend.ID)
	stateBefore, err := r.onUpdatesGetState(friendCtx)
	if err != nil {
		t.Fatalf("updates.getState before read: %v", err)
	}
	readOK, err := r.onChannelsReadHistory(friendCtx, &tg.ChannelsReadHistoryRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MaxID:   msg.ID,
	})
	if err != nil {
		t.Fatalf("channels.readHistory: %v", err)
	}
	if !readOK {
		t.Fatalf("channels.readHistory = false, want true")
	}
	stateAfter, err := r.onUpdatesGetState(friendCtx)
	if err != nil {
		t.Fatalf("updates.getState after read: %v", err)
	}
	if stateAfter.Pts != stateBefore.Pts {
		t.Fatalf("account pts after channel read = %d, want unchanged %d", stateAfter.Pts, stateBefore.Pts)
	}
	diff, err := r.onUpdatesGetDifference(friendCtx, &tg.UpdatesGetDifferenceRequest{Pts: stateBefore.Pts})
	if err != nil {
		t.Fatalf("updates.getDifference after read: %v", err)
	}
	if _, ok := diff.(*tg.UpdatesDifferenceEmpty); !ok {
		t.Fatalf("account difference after channel read = %T %+v, want empty", diff, diff)
	}
	items := deliveryOutbox.Snapshot()
	if len(items) != 2 {
		t.Fatalf("channel read durable deliveries = %+v, want reader and sender", items)
	}
	friendItem := requireDeliveryItemForUser(t, items, friend.ID)
	durable := requireDeliveryUpdates(t, friendItem)
	if len(durable.Updates) != 1 {
		t.Fatalf("reader durable updates = %+v, want one read inbox update", durable.Updates)
	}
	read, ok := durable.Updates[0].(*tg.UpdateReadChannelInbox)
	if !ok || read.ChannelID != channel.ID || read.MaxID != msg.ID || read.StillUnreadCount != 0 {
		t.Fatalf("durable update = %#v, want updateReadChannelInbox channel %d max %d", durable.Updates[0], channel.ID, msg.ID)
	}
	if read.Pts != newUpdate.Pts {
		t.Fatalf("durable channel read pts = %d, want channel pts %d", read.Pts, newUpdate.Pts)
	}
}

func TestChannelDifferenceTooLongCarriesDialogPts(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550002161", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 62, Phone: "15550002162", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "RPC TooLong Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	sourceCreated, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "RPC TooLong Source",
		About:     "forward source",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create source channel: %v", err)
	}
	sourceChannel := sourceCreated.(*tg.Updates).Chats[0].(*tg.Channel)
	if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: sourceChannel.ID, AccessHash: sourceChannel.AccessHash},
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
	}); err != nil {
		t.Fatalf("invite friend to source channel: %v", err)
	}
	sourceSent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: sourceChannel.ID, AccessHash: sourceChannel.AccessHash},
		Message:  "forward source",
		RandomID: 7000,
	})
	if err != nil {
		t.Fatalf("send source channel message: %v", err)
	}
	sourceMsgID := sourceSent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
	if _, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerChannel{ChannelID: sourceChannel.ID, AccessHash: sourceChannel.AccessHash},
		ToPeer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:       []int{sourceMsgID},
		RandomID: []int64{7001},
	}); err != nil {
		t.Fatalf("forward source channel to target channel: %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
			Message:  "too long page",
			RandomID: int64(i + 1),
		}); err != nil {
			t.Fatalf("send channel message %d: %v", i, err)
		}
	}
	diff, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     0,
		Limit:   3,
	})
	if err != nil {
		t.Fatalf("channel difference: %v", err)
	}
	tooLong, ok := diff.(*tg.UpdatesChannelDifferenceTooLong)
	if !ok {
		t.Fatalf("diff = %T %+v, want channelDifferenceTooLong", diff, diff)
	}
	dialog, ok := tooLong.Dialog.(*tg.Dialog)
	if !ok {
		t.Fatalf("tooLong dialog = %T, want dialog", tooLong.Dialog)
	}
	pts, ok := dialog.GetPts()
	if !ok || pts == 0 {
		t.Fatalf("tooLong dialog pts = %d ok=%v, want current channel pts", pts, ok)
	}
	if len(tooLong.Messages) == 0 || len(tooLong.Messages) > domain.MaxChannelDifferenceTooLongMessages {
		t.Fatalf("tooLong messages = %d, want bounded latest snapshot", len(tooLong.Messages))
	}
	if len(tooLong.Chats) == 0 || tooLong.Chats[0].(*tg.Channel).ID != channel.ID {
		t.Fatalf("tooLong chats = %+v, want source channel context", tooLong.Chats)
	}
	hasSourceChannel := false
	for _, chat := range tooLong.Chats {
		if ch, ok := chat.(*tg.Channel); ok && ch.ID == sourceChannel.ID {
			hasSourceChannel = true
			break
		}
	}
	if !hasSourceChannel {
		t.Fatalf("tooLong chats = %+v, want forwarded source channel context %d", tooLong.Chats, sourceChannel.ID)
	}
	hasOwnerUser := false
	for _, user := range tooLong.Users {
		if u, ok := user.(*tg.User); ok && u.ID == owner.ID {
			hasOwnerUser = true
			break
		}
	}
	if !hasOwnerUser {
		t.Fatalf("tooLong users = %+v, want sender user context", tooLong.Users)
	}
}

func TestChannelUnreadMentionsRPCUsesMentionState(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 9101, Phone: "15550009101", FirstName: "Owner", Username: "owner_mention"})
	member, _ := userStore.Create(ctx, domain.User{AccessHash: 9102, Phone: "15550009102", FirstName: "Mentioned", Username: "mention_friend"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), clock.System)
	created, err := channelService.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "Mention RPC",
		MemberUserIDs: []int64{member.ID},
		Date:          1700009101,
	})
	if err != nil {
		t.Fatalf("create megagroup: %v", err)
	}
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     peer,
		Message:  "hello @mention_friend",
		RandomID: 9102001,
	}); err != nil {
		t.Fatalf("send mention: %v", err)
	}
	mentions, err := r.onMessagesGetUnreadMentions(WithUserID(ctx, member.ID), &tg.MessagesGetUnreadMentionsRequest{
		Peer:      peer,
		OffsetID:  1,
		AddOffset: -10,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadMentions: %v", err)
	}
	mentionMessages, _, _ := searchMessagesPayload(t, mentions)
	if len(mentionMessages) != 1 {
		t.Fatalf("messages.getUnreadMentions = %T %+v, want one unread mention", mentions, mentions)
	}
	if msg := mentionMessages[0].(*tg.Message); msg.Message != "hello @mention_friend" {
		t.Fatalf("mention message = %#v, want sent mention", msg)
	}
	read, err := r.onMessagesReadMentions(WithUserID(ctx, member.ID), &tg.MessagesReadMentionsRequest{Peer: peer})
	if err != nil {
		t.Fatalf("messages.readMentions: %v", err)
	}
	if read.Pts <= 0 || read.PtsCount != 0 || read.Offset != 0 {
		t.Fatalf("messages.readMentions = %+v, want current channel pts and no offset", read)
	}
	mentions, err = r.onMessagesGetUnreadMentions(WithUserID(ctx, member.ID), &tg.MessagesGetUnreadMentionsRequest{
		Peer:  peer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadMentions after read: %v", err)
	}
	mentionMessages, _, _ = searchMessagesPayload(t, mentions)
	if got := len(mentionMessages); got != 0 {
		t.Fatalf("unread mentions after read = %d, want 0", got)
	}
}

func TestChannelDifferenceIncludesExtraForwardSourceChannel(t *testing.T) {
	channel := domain.Channel{ID: 2000000100, AccessHash: 9010, Title: "Megagroup", Megagroup: true, Date: 1700000000, Pts: 3}
	source := domain.Channel{ID: 2000000101, AccessHash: 9011, Title: "Source", Broadcast: true, Date: 1700000000}
	got, ok := tgChannelDifference(1000000001, domain.ChannelDifference{
		Channel: channel,
		NewMessages: []domain.ChannelMessage{{
			ChannelID:    channel.ID,
			ID:           3,
			SenderUserID: 1000000002,
			From:         domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
			Date:         1700000103,
			Body:         "forwarded",
			Forward:      &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: source.ID}, Date: 1700000000},
			Pts:          3,
		}},
		Users:    []domain.User{{ID: 1000000002, AccessHash: 42, FirstName: "Bob"}},
		Channels: []domain.Channel{source},
		Pts:      3,
		Final:    true,
		Timeout:  30,
	}).(*tg.UpdatesChannelDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesChannelDifference", got)
	}
	if len(got.Users) != 1 || len(got.Chats) != 2 {
		t.Fatalf("difference users/chats = %d/%d, want 1/2", len(got.Users), len(got.Chats))
	}
	if ch, ok := got.Chats[1].(*tg.Channel); !ok || ch.ID != source.ID {
		t.Fatalf("extra chat = %#v, want source channel", got.Chats[1])
	}
}

func TestChannelReadDeliveryUsesWireChannelPtsWithoutAuxAccountPTS(t *testing.T) {
	effects, err := channelReadDeliveryEffects(1700000102)(domain.ReadChannelHistoryResult{
		ChannelID: 12345, MaxID: 27, StillUnreadCount: 2, Changed: true, Pts: 42,
		Dialog: domain.ChannelDialog{UserID: 1000000001},
	})
	if err != nil || len(effects) != 1 {
		t.Fatalf("channel read effects = %+v, %v, want one", effects, err)
	}
	decoded, err := decodeDeliveryUpdate(effects[0].Payload)
	if err != nil {
		t.Fatalf("decode channel read delivery: %v", err)
	}
	updates, ok := decoded.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("delivery = %T %+v, want one update", decoded, decoded)
	}
	update, ok := updates.Updates[0].(*tg.UpdateReadChannelInbox)
	if !ok || update.ChannelID != 12345 || update.Pts != 42 || update.MaxID != 27 || update.StillUnreadCount != 2 {
		t.Fatalf("channel read update = %#v", updates.Updates[0])
	}
}

func TestChannelMessageContentsDeliveryBundlesAbsoluteRefresh(t *testing.T) {
	const (
		viewerID  = int64(1000000001)
		channelID = int64(2000000001)
		messageID = 27
	)
	authKeyID := [8]byte{0x27}
	ctx := WithSessionID(WithAuthKeyID(context.Background(), authKeyID), 73)
	reactions := domain.ChannelMessageReactions{
		CanSeeList: true,
		Results: []domain.ChannelMessageReactionCount{{
			Reaction: domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "👍"}, Count: 1,
		}},
	}
	effects, err := (&Router{}).channelMessageContentsDeliveryEffects(ctx, viewerID, 1_700_000_102)(domain.ReadChannelMessageContentsResult{
		Channel:                         domain.Channel{ID: channelID, Megagroup: true},
		Messages:                        []domain.ChannelMessage{{ID: messageID, ChannelID: channelID, Reactions: &reactions}},
		ClearedUnreadReactionMessageIDs: []int{messageID},
	})
	if err != nil {
		t.Fatalf("build channel contents effects: %v", err)
	}
	if len(effects) != 1 || effects[0].TargetUserID != viewerID || effects[0].ExcludeAuthKeyID != authKeyID || effects[0].ExcludeSessionID != 73 {
		t.Fatalf("channel contents effects = %+v, want one viewer-scoped absolute effect", effects)
	}
	decoded, err := decodeDeliveryUpdate(effects[0].Payload)
	if err != nil {
		t.Fatalf("decode channel contents effect: %v", err)
	}
	updates := decoded.(*tg.Updates)
	if len(updates.Updates) != 2 {
		t.Fatalf("channel contents updates = %+v, want content-read and reaction refresh", updates.Updates)
	}
	if _, ok := updates.Updates[0].(*tg.UpdateChannelReadMessagesContents); !ok {
		t.Fatalf("channel contents update[0] = %T", updates.Updates[0])
	}
	if reaction, ok := updates.Updates[1].(*tg.UpdateMessageReactions); !ok || reaction.MsgID != messageID {
		t.Fatalf("channel contents update[1] = %#v, want reaction refresh for %d", updates.Updates[1], messageID)
	}
}

func TestProjectChannelMentionForViewer(t *testing.T) {
	event := domain.ChannelUpdateEvent{
		ChannelID: 500,
		Pts:       7,
		PtsCount:  1,
		Message: domain.ChannelMessage{
			ChannelID:    500,
			ID:           42,
			SenderUserID: 1001,
			Body:         "hi @bob",
			Media:        &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 9}},
		},
	}
	mentioned := projectChannelMentionForViewer(event, []int64{1002}, 1002)
	if !mentioned.Message.Mentioned || !mentioned.Message.MediaUnread {
		t.Fatalf("mentioned viewer = %+v, want mentioned+media_unread set in realtime push", mentioned.Message)
	}
	other := projectChannelMentionForViewer(event, []int64{1002}, 1003)
	if other.Message.Mentioned || other.Message.MediaUnread {
		t.Fatalf("other viewer = %+v, must not inherit mention flags", other.Message)
	}
	sender := projectChannelMentionForViewer(event, []int64{1001}, 1001)
	if sender.Message.Mentioned {
		t.Fatalf("sender = %+v, must not be marked mentioned by own message", sender.Message)
	}
	if event.Message.Mentioned {
		t.Fatalf("source event mutated: projection must copy, not alias")
	}
}
