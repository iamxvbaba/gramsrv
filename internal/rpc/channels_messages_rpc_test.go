package rpc

import (
	"context"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strconv"
	"strings"
	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appupdates "telesrv/internal/app/updates"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
	"time"
)

func TestChannelsDeleteChannelReturnsForbiddenChatAndHidesDialogRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550001031", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550001032", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	invited, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Delete Me",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := invited.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	getDialogs := func(userID int64) *tg.MessagesDialogs {
		t.Helper()
		req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}
		var b bin.Buffer
		if err := req.Encode(&b); err != nil {
			t.Fatalf("encode get dialogs: %v", err)
		}
		enc, err := r.Dispatch(WithUserID(ctx, userID), [8]byte{}, 0, &b)
		if err != nil {
			t.Fatalf("dispatch get dialogs: %v", err)
		}
		dialogs, ok := enc.(*tg.MessagesDialogs)
		if !ok {
			t.Fatalf("dialogs response = %T, want *tg.MessagesDialogs", enc)
		}
		return dialogs
	}
	if got := getDialogs(owner.ID); len(got.Dialogs) != 1 || len(got.Chats) != 1 {
		t.Fatalf("dialogs before delete = %+v, want one channel dialog", got)
	}

	deleted, err := r.onChannelsDeleteChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{
		ChannelID:  channel.ID,
		AccessHash: channel.AccessHash,
	})
	if err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	updates, ok := deleted.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 || len(updates.Chats) != 1 {
		t.Fatalf("delete response = %T %+v, want updateChannel + channelForbidden", deleted, deleted)
	}
	if update, ok := updates.Updates[0].(*tg.UpdateChannel); !ok || update.ChannelID != channel.ID {
		t.Fatalf("delete update = %#v, want updateChannel %d", updates.Updates[0], channel.ID)
	}
	forbidden, ok := updates.Chats[0].(*tg.ChannelForbidden)
	if !ok || forbidden.ID != channel.ID || forbidden.AccessHash != channel.AccessHash || forbidden.Title != channel.Title || !forbidden.Megagroup {
		t.Fatalf("delete chat = %#v, want channelForbidden tombstone", updates.Chats[0])
	}

	pushed := sessions.snapshot()
	if pushed.messageType != proto.MessageFromServer || pushed.userID == 0 {
		t.Fatalf("push snapshot = %+v, want server update to a channel member", pushed)
	}
	pushedUpdates, ok := pushed.message.(*tg.Updates)
	if !ok || len(pushedUpdates.Chats) != 1 {
		t.Fatalf("pushed update = %T %+v, want channelForbidden chat", pushed.message, pushed.message)
	}
	if pushedForbidden, ok := pushedUpdates.Chats[0].(*tg.ChannelForbidden); !ok || pushedForbidden.ID != channel.ID {
		t.Fatalf("pushed chat = %#v, want channelForbidden %d", pushedUpdates.Chats[0], channel.ID)
	}
	if got := getDialogs(owner.ID); len(got.Dialogs) != 0 || len(got.Chats) != 0 {
		t.Fatalf("owner dialogs after delete = %+v, want hidden deleted channel", got)
	}
	if got := getDialogs(friend.ID); len(got.Dialogs) != 0 || len(got.Chats) != 0 {
		t.Fatalf("friend dialogs after delete = %+v, want hidden deleted channel", got)
	}
}

func TestChannelGetMessagesReturnsSparseIDs(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550002101", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550002102", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Sparse RPC Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	var firstID, lastID int
	for i := 0; i < 5; i++ {
		updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
			Message:  "sparse-" + strconv.Itoa(i),
			RandomID: int64(100 + i),
		})
		if err != nil {
			t.Fatalf("send channel message %d: %v", i, err)
		}
		msg := updates.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
		if i == 0 {
			firstID = msg.ID
		}
		lastID = msg.ID
	}

	got, err := r.onChannelsGetMessages(WithUserID(ctx, friend.ID), &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID: []tg.InputMessageClass{
			&tg.InputMessageID{ID: firstID},
			&tg.InputMessageID{ID: lastID},
		},
	})
	if err != nil {
		t.Fatalf("get sparse channel messages: %v", err)
	}
	messages := got.(*tg.MessagesMessages).Messages
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(messages))
	}
	first := messages[0].(*tg.Message)
	last := messages[1].(*tg.Message)
	if first.ID != firstID || first.Message != "sparse-0" || last.ID != lastID || last.Message != "sparse-4" {
		t.Fatalf("sparse messages = %#v %#v, want first and last exact ids", first, last)
	}
}

func TestChannelsDeleteHistoryLocalClearEmitsAvailableMessagesUpdate(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550002141", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550002142", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	updateSvc := appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore())
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Updates:  updateSvc,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Clear Channel",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "clear me",
		RandomID: 401,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	msg := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	cleared, err := r.onChannelsDeleteHistory(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteHistoryRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MaxID:   msg.ID,
	})
	if err != nil {
		t.Fatalf("delete channel history local: %v", err)
	}
	updates, ok := cleared.(*tg.Updates)
	if !ok || len(updates.Updates) != 2 {
		t.Fatalf("clear response = %T %+v, want available update plus pts bookkeeping", cleared, cleared)
	}
	available, ok := updates.Updates[0].(*tg.UpdateChannelAvailableMessages)
	if !ok || available.ChannelID != channel.ID || available.AvailableMinID != msg.ID {
		t.Fatalf("clear update = %#v, want updateChannelAvailableMessages channel=%d min=%d", updates.Updates[0], channel.ID, msg.ID)
	}
	// updateChannelAvailableMessages 不带账号 pts，事件占用的 pts 槽位
	// 必须用空 updateDeleteMessages 显式同步给客户端。
	bookkeeping, ok := updates.Updates[1].(*tg.UpdateDeleteMessages)
	if !ok || len(bookkeeping.Messages) != 0 || bookkeeping.Pts <= 0 || bookkeeping.PtsCount != 1 {
		t.Fatalf("clear bookkeeping = %#v, want empty updateDeleteMessages carrying the account pts step", updates.Updates[1])
	}
	pushed := sessions.snapshot()
	if pushed.userID != owner.ID {
		t.Fatalf("pushed user = %d, want owner %d", pushed.userID, owner.ID)
	}
	pushedUpdates, ok := pushed.message.(*tg.Updates)
	if !ok || len(pushedUpdates.Updates) != 2 {
		t.Fatalf("pushed clear update = %T %+v, want available update plus pts bookkeeping", pushed.message, pushed.message)
	}
	if _, ok := pushedUpdates.Updates[0].(*tg.UpdateChannelAvailableMessages); !ok {
		t.Fatalf("pushed update[0] = %T, want updateChannelAvailableMessages", pushedUpdates.Updates[0])
	}
	diff, err := r.onUpdatesGetDifference(WithUserID(ctx, owner.ID), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference: %v", err)
	}
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok || len(full.OtherUpdates) != 1 {
		t.Fatalf("difference = %T %+v, want one other update", diff, diff)
	}
	if diffUpdate, ok := full.OtherUpdates[0].(*tg.UpdateChannelAvailableMessages); !ok || diffUpdate.ChannelID != channel.ID || diffUpdate.AvailableMinID != msg.ID {
		t.Fatalf("difference update = %#v, want updateChannelAvailableMessages", full.OtherUpdates[0])
	}

	stale, err := r.onChannelsDeleteHistory(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteHistoryRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MaxID:   msg.ID - 1,
	})
	if err != nil {
		t.Fatalf("stale delete channel history local: %v", err)
	}
	staleUpdates, ok := stale.(*tg.Updates)
	if !ok || len(staleUpdates.Updates) != 2 {
		t.Fatalf("stale clear response = %T %+v, want monotonic update plus pts bookkeeping", stale, stale)
	}
	staleAvailable, ok := staleUpdates.Updates[0].(*tg.UpdateChannelAvailableMessages)
	if !ok || staleAvailable.ChannelID != channel.ID || staleAvailable.AvailableMinID != msg.ID {
		t.Fatalf("stale clear update = %#v, want monotonic updateChannelAvailableMessages channel=%d min=%d", staleUpdates.Updates[0], channel.ID, msg.ID)
	}
}

func TestChannelDeleteRejectsInvalidMessageIDsRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 43, Phone: "15550002143", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 44, Phone: "15550002144", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Invalid Delete IDs",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	for _, maxID := range []int{-1, domain.MaxMessageBoxID + 1} {
		if _, err := r.onChannelsDeleteHistory(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteHistoryRequest{Channel: input, MaxID: maxID}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
			t.Fatalf("channels.deleteHistory max_id=%d err = %v, want MESSAGE_ID_INVALID", maxID, err)
		}
		if _, err := r.onMessagesDeleteHistory(WithUserID(ctx, owner.ID), &tg.MessagesDeleteHistoryRequest{Peer: peer, MaxID: maxID}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
			t.Fatalf("messages.deleteHistory channel max_id=%d err = %v, want MESSAGE_ID_INVALID", maxID, err)
		}
	}
	if _, err := r.onChannelsDeleteMessages(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteMessagesRequest{Channel: input, ID: []int{domain.MaxMessageBoxID + 1}}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("channels.deleteMessages huge id err = %v, want MESSAGE_ID_INVALID", err)
	}
	tooMany := make([]int, domain.MaxDeleteMessageIDs+1)
	for i := range tooMany {
		tooMany[i] = i + 1
	}
	if _, err := r.onChannelsDeleteMessages(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteMessagesRequest{Channel: input, ID: tooMany}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("channels.deleteMessages too many ids err = %v, want LIMIT_INVALID", err)
	}
}

func TestChannelsDeleteHistoryForEveryoneDrainsBatchesAndKeepsDialogVisible(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 45, Phone: "15550002145", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 46, Phone: "15550002146", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Drain Clear",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	total := domain.MaxDeleteHistoryBatch + 1
	for i := 0; i < total; i++ {
		if _, err := channelStore.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    owner.ID,
			ChannelID: channel.ID,
			RandomID:  int64(50_000 + i),
			Message:   "drain",
			Date:      1_700_000_400 + i,
		}); err != nil {
			t.Fatalf("seed channel message %d: %v", i, err)
		}
	}
	pushedBefore := len(sessions.pushedUserIDs())

	clearReq := &tg.ChannelsDeleteHistoryRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}
	clearReq.SetForEveryone(true)
	cleared, err := r.onChannelsDeleteHistory(WithUserID(ctx, owner.ID), clearReq)
	if err != nil {
		t.Fatalf("delete channel history for everyone: %v", err)
	}
	updates, ok := cleared.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("clear response = %T %+v, want final delete batch updates", cleared, cleared)
	}
	// 最后一批删除 invite 服务消息与剩余文本；id=1 建群服务消息保留为
	// 会话兜底 top message，不进入删除批次。
	deleteUpdate, ok := updates.Updates[0].(*tg.UpdateDeleteChannelMessages)
	if !ok || deleteUpdate.ChannelID != channel.ID || deleteUpdate.PtsCount != len(deleteUpdate.Messages) || len(deleteUpdate.Messages) == 0 {
		t.Fatalf("final batch update = %#v, want trailing delete batch sparing the creation service message", updates.Updates[0])
	}
	for _, id := range deleteUpdate.Messages {
		if id <= 1 {
			t.Fatalf("final batch deleted id %d, creation service message must be spared", id)
		}
	}
	// 两批（1000+1）删除，每批向 owner 和 friend 各推送一次。
	if pushedAfter := len(sessions.pushedUserIDs()); pushedAfter-pushedBefore != 4 {
		t.Fatalf("pushed fanout count = %d, want 4 (two batches to both members)", pushedAfter-pushedBefore)
	}
	history, err := channelStore.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list history after clear: %v", err)
	}
	if len(history.Messages) != 1 || history.Messages[0].ID != 1 || history.Messages[0].Action == nil {
		t.Fatalf("history after clear = %+v, want only create service message", history.Messages)
	}
	getDialogs := func(userID int64) *tg.MessagesDialogs {
		t.Helper()
		req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}
		var b bin.Buffer
		if err := req.Encode(&b); err != nil {
			t.Fatalf("encode get dialogs: %v", err)
		}
		enc, err := r.Dispatch(WithUserID(ctx, userID), [8]byte{}, 0, &b)
		if err != nil {
			t.Fatalf("dispatch get dialogs: %v", err)
		}
		dialogs, ok := enc.(*tg.MessagesDialogs)
		if !ok {
			t.Fatalf("dialogs response = %T, want messages.dialogs", enc)
		}
		return dialogs
	}
	for _, viewer := range []int64{owner.ID, friend.ID} {
		got := getDialogs(viewer)
		if len(got.Dialogs) != 1 || len(got.Chats) != 1 {
			t.Fatalf("dialogs for %d after clear = %+v, want channel dialog kept visible", viewer, got)
		}
		dialog := got.Dialogs[0].(*tg.Dialog)
		if dialog.TopMessage != 1 {
			t.Fatalf("dialog top for %d = %d, want create service message 1", viewer, dialog.TopMessage)
		}
		if len(got.Messages) != 1 {
			t.Fatalf("dialog top messages for %d = %+v, want create service message attached", viewer, got.Messages)
		}
		if _, ok := got.Messages[0].(*tg.MessageService); !ok {
			t.Fatalf("dialog top message for %d = %T, want messageService", viewer, got.Messages[0])
		}
	}

	againReq := &tg.ChannelsDeleteHistoryRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}
	againReq.SetForEveryone(true)
	again, err := r.onChannelsDeleteHistory(WithUserID(ctx, owner.ID), againReq)
	if err != nil {
		t.Fatalf("repeat delete channel history: %v", err)
	}
	if repeat, ok := again.(*tg.Updates); !ok || len(repeat.Updates) != 0 {
		t.Fatalf("repeat clear response = %T %+v, want empty idempotent updates", again, again)
	}
}

func TestBroadcastChannelPostRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 71, Phone: "15550002171", FirstName: "Owner"})
	member, _ := userStore.Create(ctx, domain.User{AccessHash: 72, Phone: "15550002172", FirstName: "Member"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "RPC Broadcast",
		About:     "news",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	if !channel.Broadcast || channel.Megagroup {
		t.Fatalf("channel flags = broadcast:%v megagroup:%v, want broadcast only", channel.Broadcast, channel.Megagroup)
	}
	if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: member.ID, AccessHash: member.AccessHash}},
	}); err != nil {
		t.Fatalf("invite member to broadcast: %v", err)
	}
	posted, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "broadcast post",
		RandomID: 7071,
	})
	if err != nil {
		t.Fatalf("owner send broadcast post: %v", err)
	}
	postUpdate := posted.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage)
	postMsg := postUpdate.Message.(*tg.Message)
	if !postMsg.Post || postMsg.FromID != nil || postMsg.Message != "broadcast post" {
		t.Fatalf("broadcast post message = %#v, want post without from_id", postMsg)
	}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, member.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "member post",
		RandomID: 7072,
	}); err == nil || !strings.Contains(err.Error(), "CHAT_WRITE_FORBIDDEN") {
		t.Fatalf("member broadcast send err = %v, want CHAT_WRITE_FORBIDDEN", err)
	}
	if _, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel:     &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:      &tg.InputUser{UserID: member.ID, AccessHash: member.AccessHash},
		AdminRights: tg.ChatAdminRights{ChangeInfo: true},
	}); err != nil {
		t.Fatalf("promote member without post_messages: %v", err)
	}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, member.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "admin without post right",
		RandomID: 7073,
	}); err == nil || !strings.Contains(err.Error(), "CHAT_WRITE_FORBIDDEN") {
		t.Fatalf("admin without post right send err = %v, want CHAT_WRITE_FORBIDDEN", err)
	}
	if _, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel:     &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:      &tg.InputUser{UserID: member.ID, AccessHash: member.AccessHash},
		AdminRights: tg.ChatAdminRights{PostMessages: true},
	}); err != nil {
		t.Fatalf("grant member post_messages: %v", err)
	}
	adminPosted, err := r.onMessagesSendMessage(WithUserID(ctx, member.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "admin post",
		RandomID: 7074,
	})
	if err != nil {
		t.Fatalf("admin send broadcast post: %v", err)
	}
	adminPostMsg := adminPosted.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if !adminPostMsg.Post || adminPostMsg.FromID != nil || adminPostMsg.Message != "admin post" {
		t.Fatalf("admin broadcast post message = %#v, want post without from_id", adminPostMsg)
	}
	diff, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, member.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     1,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("member channel difference: %v", err)
	}
	fullDiff := diff.(*tg.UpdatesChannelDifference)
	foundPost := false
	for _, msg := range fullDiff.NewMessages {
		if item, ok := msg.(*tg.Message); ok && item.Message == "broadcast post" {
			foundPost = item.Post && item.FromID == nil
		}
	}
	if !foundPost {
		t.Fatalf("diff new messages = %+v, want broadcast post without from_id", fullDiff.NewMessages)
	}
}

func TestChannelSendMessageRPCResolvesReplyHeader(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550002031", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550002032", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "RPC Reply Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	rootUpdates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "root",
		RandomID: 3001,
	})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	rootMsg := rootUpdates.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	replyTo := &tg.InputReplyToMessage{ReplyToMsgID: rootMsg.ID}
	replyTo.SetQuoteText("root")
	replyTo.SetQuoteOffset(0)
	replyReq := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "reply",
		RandomID: 3002,
	}
	replyReq.SetReplyTo(replyTo)
	replyUpdates, err := r.onMessagesSendMessage(WithUserID(ctx, friend.ID), replyReq)
	if err != nil {
		t.Fatalf("send reply: %v", err)
	}
	replyMsg := replyUpdates.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	header, ok := replyMsg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok {
		t.Fatalf("reply header = %#v, want messageReplyHeader", replyMsg.ReplyTo)
	}
	topID, topOK := header.GetReplyToTopID()
	quoteText, quoteOK := header.GetQuoteText()
	if header.ReplyToMsgID != rootMsg.ID || !topOK || topID != rootMsg.ID || !quoteOK || quoteText != "root" {
		t.Fatalf("reply header = %#v, want msg/top %d and quote", header, rootMsg.ID)
	}
	badReq := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "bad",
		RandomID: 3003,
	}
	badReq.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: 999})
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), badReq); err == nil || !strings.Contains(err.Error(), "REPLY_MESSAGE_ID_INVALID") {
		t.Fatalf("bad reply err = %v, want REPLY_MESSAGE_ID_INVALID", err)
	}
}

func TestChannelBoostUnlocksDefaultSendRestrictionRPC(t *testing.T) {
	f := newRPCChannelFixture(t)
	r := f.router
	owner := f.user(9101, "15550009101", "Owner")
	member := f.user(9102, "15550009102", "Member")
	channel := f.createLegacyMegagroup(owner, "Boost Gate", member)
	ownerCtx := f.userCtx(owner)
	memberCtx := f.userCtx(member)

	if _, err := r.onMessagesEditChatDefaultBannedRights(ownerCtx, &tg.MessagesEditChatDefaultBannedRightsRequest{
		Peer: inputPeerChannel(channel),
		BannedRights: tg.ChatBannedRights{
			SendMessages: true,
		},
	}); err != nil {
		t.Fatalf("edit default banned rights: %v", err)
	}
	if _, err := r.onMessagesSendMessage(memberCtx, &tg.MessagesSendMessageRequest{
		Peer:     inputPeerChannel(channel),
		Message:  "blocked",
		RandomID: 9102001,
	}); err == nil || !strings.Contains(err.Error(), "CHAT_WRITE_FORBIDDEN") {
		t.Fatalf("member send before boost err = %v, want CHAT_WRITE_FORBIDDEN", err)
	}
	if _, err := r.onChannelsSetBoostsToUnblockRestrictions(ownerCtx, &tg.ChannelsSetBoostsToUnblockRestrictionsRequest{
		Channel: inputChannel(channel),
		Boosts:  1,
	}); err != nil {
		t.Fatalf("set boosts to unblock: %v", err)
	}
	if _, err := f.users.SetPremiumUntil(f.ctx, member.ID, int(time.Now().Add(time.Hour).Unix())); err != nil {
		t.Fatalf("grant member premium: %v", err)
	}
	applyReq := &tg.PremiumApplyBoostRequest{Peer: inputPeerChannel(channel)}
	applyReq.SetSlots([]int{domain.DefaultPremiumBoostSlotID})
	if _, err := r.onPremiumApplyBoost(memberCtx, applyReq); err != nil {
		t.Fatalf("apply member boost: %v", err)
	}
	full, err := r.onChannelsGetFullChannel(memberCtx, inputChannel(channel))
	if err != nil {
		t.Fatalf("get full channel after boost: %v", err)
	}
	channelFull := full.FullChat.(*tg.ChannelFull)
	if boosts, ok := channelFull.GetBoostsApplied(); !ok || boosts != 1 {
		t.Fatalf("full channel boosts_applied = %d ok %v, want 1", boosts, ok)
	}
	if threshold, ok := channelFull.GetBoostsUnrestrict(); !ok || threshold != 1 {
		t.Fatalf("full channel boosts_unrestrict = %d ok %v, want 1", threshold, ok)
	}
	sent, err := r.onMessagesSendMessage(memberCtx, &tg.MessagesSendMessageRequest{
		Peer:     inputPeerChannel(channel),
		Message:  "boosted ok",
		RandomID: 9102002,
	})
	if err != nil {
		t.Fatalf("member send after boost: %v", err)
	}
	msg := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if applied, ok := msg.GetFromBoostsApplied(); !ok || applied != 1 {
		t.Fatalf("message from_boosts_applied = %d ok %v, want 1", applied, ok)
	}
}

func TestChannelSendAsCurrentChannelRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550002111", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 52, Phone: "15550002112", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Send As Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	sendAs := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "as channel",
		RandomID: 501,
		SendAs:   sendAs,
	})
	if err != nil {
		t.Fatalf("send as current channel: %v", err)
	}
	sentUpdates := sent.(*tg.Updates)
	sentMsg := sentUpdates.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if from, ok := sentMsg.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("send_as message from = %#v, want channel %d", sentMsg.FromID, channel.ID)
	}

	history, err := r.onChannelsGetMessages(WithUserID(ctx, friend.ID), &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: sentMsg.ID}},
	})
	if err != nil {
		t.Fatalf("get send_as history: %v", err)
	}
	historyMsg := history.(*tg.MessagesMessages).Messages[0].(*tg.Message)
	if from, ok := historyMsg.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("history send_as from = %#v, want channel %d", historyMsg.FromID, channel.ID)
	}

	forwarded, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ToPeer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:       []int{sentMsg.ID},
		RandomID: []int64{502},
		SendAs:   sendAs,
	})
	if err != nil {
		t.Fatalf("forward as current channel: %v", err)
	}
	forwardMsg := forwarded.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if from, ok := forwardMsg.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("forward send_as from = %#v, want channel %d", forwardMsg.FromID, channel.ID)
	}

	if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  "bad private send_as",
		RandomID: 503,
		SendAs:   sendAs,
	}); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("private send_as err = %v, want SEND_AS_PEER_INVALID", err)
	}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, friend.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "bad member send_as",
		RandomID: 504,
		SendAs:   sendAs,
	}); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("member send_as err = %v, want SEND_AS_PEER_INVALID", err)
	}
	if _, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ToPeer:   &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		ID:       []int{sentMsg.ID},
		RandomID: []int64{505},
		SendAs:   sendAs,
	}); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("private forward send_as err = %v, want SEND_AS_PEER_INVALID", err)
	}
}

func TestChannelsSearchPostsReturnsPublicPostsWithSeekPaging(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 91001, Phone: "15550091001", FirstName: "Owner"})
	viewer, _ := userStore.Create(ctx, domain.User{AccessHash: 91002, Phone: "15550091002", FirstName: "Viewer"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), clock.System)
	public, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Public Search",
		Broadcast: true,
		Date:      1700010000,
	})
	if err != nil {
		t.Fatalf("create public channel: %v", err)
	}
	if _, err := channelService.UpdateUsername(ctx, owner.ID, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: public.Channel.ID,
		Username:  "public_search_posts",
	}); err != nil {
		t.Fatalf("publish channel username: %v", err)
	}
	private, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Private Search",
		Broadcast: true,
		Date:      1700010001,
	})
	if err != nil {
		t.Fatalf("create private channel: %v", err)
	}
	if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: public.Channel.ID,
		RandomID:  101,
		Message:   "launch alpha #ops",
		Date:      1700010010,
	}); err != nil {
		t.Fatalf("send public first: %v", err)
	}
	if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: public.Channel.ID,
		RandomID:  102,
		Message:   "launch beta #ops",
		Date:      1700010020,
	}); err != nil {
		t.Fatalf("send public second: %v", err)
	}
	if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: private.Channel.ID,
		RandomID:  103,
		Message:   "launch private #ops",
		Date:      1700010030,
	}); err != nil {
		t.Fatalf("send private: %v", err)
	}

	req := &tg.ChannelsSearchPostsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      1,
	}
	req.SetQuery("launch")
	got, err := r.onChannelsSearchPosts(WithUserID(ctx, viewer.ID), req)
	if err != nil {
		t.Fatalf("channels.searchPosts first page: %v", err)
	}
	slice, ok := got.(*tg.MessagesMessagesSlice)
	if !ok {
		t.Fatalf("first page = %T %+v, want messagesSlice", got, got)
	}
	if slice.Count <= len(slice.Messages) {
		t.Fatalf("first page count = %d messages=%d, want more page", slice.Count, len(slice.Messages))
	}
	if nextRate, ok := slice.GetNextRate(); !ok || nextRate != 1700010020 {
		t.Fatalf("first page next_rate = %d ok %v, want newest message date", nextRate, ok)
	}
	if flood, ok := slice.GetSearchFlood(); !ok || !flood.QueryIsFree {
		t.Fatalf("first page search_flood = %+v ok %v, want free flood state", flood, ok)
	}
	messages, chats, users := searchMessagesPayload(t, got)
	if len(messages) != 1 || len(chats) != 1 || len(users) != 1 {
		t.Fatalf("first page payload messages=%d chats=%d users=%d, want 1/1/1", len(messages), len(chats), len(users))
	}
	first := messages[0].(*tg.Message)
	if first.Message != "launch beta #ops" {
		t.Fatalf("first result message = %q, want newest public hit", first.Message)
	}
	if peer, ok := first.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != public.Channel.ID {
		t.Fatalf("first result peer = %#v, want public channel %d", first.PeerID, public.Channel.ID)
	}

	page2 := &tg.ChannelsSearchPostsRequest{
		OffsetRate: slice.NextRate,
		OffsetPeer: &tg.InputPeerChannel{
			ChannelID:  public.Channel.ID,
			AccessHash: public.Channel.AccessHash,
		},
		OffsetID: first.ID,
		Limit:    10,
	}
	page2.SetQuery("launch")
	got, err = r.onChannelsSearchPosts(WithUserID(ctx, viewer.ID), page2)
	if err != nil {
		t.Fatalf("channels.searchPosts second page: %v", err)
	}
	messages, chats, _ = searchMessagesPayload(t, got)
	if len(messages) != 1 || len(chats) != 1 {
		t.Fatalf("second page payload messages=%d chats=%d, want only older public hit", len(messages), len(chats))
	}
	if msg := messages[0].(*tg.Message); msg.Message != "launch alpha #ops" {
		t.Fatalf("second result message = %q, want older public hit", msg.Message)
	}

	hashtagReq := &tg.ChannelsSearchPostsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      10,
	}
	hashtagReq.SetHashtag("ops")
	got, err = r.onChannelsSearchPosts(WithUserID(ctx, viewer.ID), hashtagReq)
	if err != nil {
		t.Fatalf("channels.searchPosts hashtag: %v", err)
	}
	messages, _, _ = searchMessagesPayload(t, got)
	if len(messages) != 2 {
		t.Fatalf("hashtag results = %d, want two public hits only", len(messages))
	}
	for _, item := range messages {
		if strings.Contains(item.(*tg.Message).Message, "private") {
			t.Fatalf("hashtag leaked private message: %#v", item)
		}
	}
}

func TestChannelsSearchPostsValidatesStubBounds(t *testing.T) {
	const userID = int64(1000000001)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)

	valid := &tg.ChannelsSearchPostsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      20,
	}
	valid.SetQuery("launch")
	got, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), valid)
	if err != nil {
		t.Fatalf("channels.searchPosts valid stub: %v", err)
	}
	if page, ok := got.(*tg.MessagesMessages); !ok || len(page.Messages) != 0 || len(page.Chats) != 0 || len(page.Users) != 0 {
		t.Fatalf("channels.searchPosts = %T %+v, want empty messages", got, got)
	}

	both := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 1}
	both.SetQuery("launch")
	both.SetHashtag("launch")
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), both); err == nil || !strings.Contains(err.Error(), "SEARCH_QUERY_EMPTY") {
		t.Fatalf("channels.searchPosts query+hashtag err = %v, want SEARCH_QUERY_EMPTY", err)
	}
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 1}); err == nil || !strings.Contains(err.Error(), "SEARCH_QUERY_EMPTY") {
		t.Fatalf("channels.searchPosts empty query err = %v, want SEARCH_QUERY_EMPTY", err)
	}

	huge := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 1}
	huge.SetQuery(strings.Repeat("x", maxChannelSearchPostsQuery+1))
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), huge); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("channels.searchPosts huge query err = %v, want LIMIT_INVALID", err)
	}

	badOffset := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 1, OffsetID: domain.MaxMessageBoxID + 1}
	badOffset.SetHashtag("launch")
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), badOffset); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("channels.searchPosts huge offset_id err = %v, want MESSAGE_ID_INVALID", err)
	}

	badStars := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 1, AllowPaidStars: -1}
	badStars.SetQuery("launch")
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), badStars); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("channels.searchPosts negative stars err = %v, want LIMIT_INVALID", err)
	}

	badPeer := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerUser{UserID: 42}, Limit: 1}
	badPeer.SetQuery("launch")
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), badPeer); err == nil || !strings.Contains(err.Error(), "PEER_ID_INVALID") {
		t.Fatalf("channels.searchPosts user offset peer err = %v, want PEER_ID_INVALID", err)
	}

	floodReq := &tg.ChannelsCheckSearchPostsFloodRequest{}
	floodReq.SetQuery("launch")
	flood, err := r.onChannelsCheckSearchPostsFlood(WithUserID(context.Background(), userID), floodReq)
	if err != nil {
		t.Fatalf("channels.checkSearchPostsFlood: %v", err)
	}
	if !flood.QueryIsFree || flood.Remains <= 0 || flood.TotalDaily <= 0 {
		t.Fatalf("channels.checkSearchPostsFlood = %+v, want free quota", flood)
	}

	if _, err := r.onChannelsCheckSearchPostsFlood(WithUserID(context.Background(), userID), &tg.ChannelsCheckSearchPostsFloodRequest{}); err == nil || !strings.Contains(err.Error(), "SEARCH_QUERY_EMPTY") {
		t.Fatalf("channels.checkSearchPostsFlood empty query err = %v, want SEARCH_QUERY_EMPTY", err)
	}
	floodHuge := &tg.ChannelsCheckSearchPostsFloodRequest{}
	floodHuge.SetQuery(strings.Repeat("x", maxChannelSearchPostsQuery+1))
	if _, err := r.onChannelsCheckSearchPostsFlood(WithUserID(context.Background(), userID), floodHuge); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("channels.checkSearchPostsFlood huge query err = %v, want LIMIT_INVALID", err)
	}
}
