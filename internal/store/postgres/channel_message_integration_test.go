package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"telesrv/internal/domain"
	"testing"
	"time"
)

func TestChannelStoreSendMessageFansOutDialogRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 31,
		Phone:      "+1777" + suffix + "01",
		FirstName:  "ChannelOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{
		AccessHash: 32,
		Phone:      "+1777" + suffix + "02",
		FirstName:  "ChannelFriend",
	})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Dialog Top " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID},
		Date:          1700000300,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  901,
		Message:   "first visible channel text",
		Date:      1700000301,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}

	// H4a：发送事务不再对全员 upsert channel_dialogs——dialog 投影读时派生。
	// friend 视角：top 来自 channels.top_message_id，unread 由可见 incoming 消息动态重算。
	friendView, err := channels.GetChannelDialogs(ctx, friend.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get friend dialogs after send: %v", err)
	}
	if len(friendView.Dialogs) != 1 || friendView.Dialogs[0].TopMessage != sent.Message.ID ||
		friendView.Dialogs[0].ReadInboxMaxID != 0 || friendView.Dialogs[0].UnreadCount != 2 {
		t.Fatalf("friend derived dialog = %+v, want top %d read 0 unread 2", friendView.Dialogs, sent.Message.ID)
	}

	// 发送者自己的 outbox 回执水位不得随发送推进：同账号另一设备会把它当成
	// "对端已读"渲染双勾。推进只能来自 peer 的 readHistory（见下方断言）。
	ownerViewBefore, err := channels.GetChannelDialogs(ctx, owner.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get owner dialogs after send: %v", err)
	}
	if len(ownerViewBefore.Dialogs) != 1 || ownerViewBefore.Dialogs[0].TopMessage != sent.Message.ID ||
		ownerViewBefore.Dialogs[0].ReadInboxMaxID != sent.Message.ID ||
		ownerViewBefore.Dialogs[0].ReadOutboxMaxID != 0 || ownerViewBefore.Dialogs[0].UnreadCount != 0 {
		t.Fatalf("owner derived dialog = %+v, want top/read_in %d, read_out 0 before peer read, unread 0", ownerViewBefore.Dialogs, sent.Message.ID)
	}

	var ownerMemberReadInbox, ownerMemberReadOutbox int
	if err := pool.QueryRow(ctx, `
SELECT read_inbox_max_id, read_outbox_max_id
FROM channel_members
WHERE channel_id = $1 AND user_id = $2`, channelID, owner.ID).Scan(&ownerMemberReadInbox, &ownerMemberReadOutbox); err != nil {
		t.Fatalf("read owner member row after send: %v", err)
	}
	if ownerMemberReadInbox != sent.Message.ID || ownerMemberReadOutbox != 0 {
		t.Fatalf("owner member read_in=%d read_out=%d, want read_in %d and read_out unchanged before peer read", ownerMemberReadInbox, ownerMemberReadOutbox, sent.Message.ID)
	}

	dialogs, err := channels.ListChannelDialogs(ctx, friend.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list friend dialogs: %v", err)
	}
	if len(dialogs.Dialogs) != 1 || dialogs.Dialogs[0].TopMessage != sent.Message.ID {
		t.Fatalf("friend dialogs = %+v, want top message %d", dialogs.Dialogs, sent.Message.ID)
	}
	if len(dialogs.Messages) != 1 || dialogs.Messages[0].Body != "first visible channel text" {
		t.Fatalf("friend dialog messages = %+v, want latest channel text", dialogs.Messages)
	}
	if dialogs.Dialogs[0].UnreadCount != 2 {
		t.Fatalf("friend unread = %d, want create service + latest text", dialogs.Dialogs[0].UnreadCount)
	}

	read, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    friend.ID,
		ChannelID: channelID,
		MaxID:     sent.Message.ID,
		Date:      1700000302,
	})
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if read.Dialog.UnreadCount != 0 || read.Dialog.ReadInboxMaxID != sent.Message.ID {
		t.Fatalf("read dialog = %+v, want fully read through latest", read.Dialog)
	}
	if len(read.OutboxUpdates) != 1 || read.OutboxUpdates[0].UserID != owner.ID || read.OutboxUpdates[0].MaxID != sent.Message.ID {
		t.Fatalf("read outbox updates = %+v, want owner max id %d", read.OutboxUpdates, sent.Message.ID)
	}
	ownerView, err := channels.GetChannel(ctx, owner.ID, channelID)
	if err != nil {
		t.Fatalf("get owner channel after read: %v", err)
	}
	if ownerView.Dialog.ReadOutboxMaxID != sent.Message.ID {
		t.Fatalf("owner dialog read_outbox = %d, want %d", ownerView.Dialog.ReadOutboxMaxID, sent.Message.ID)
	}
	if changed, folderID, err := channels.SetChannelDialogPinned(ctx, owner.ID, channelID, true); err != nil || !changed || folderID != domain.DialogMainFolderID {
		t.Fatalf("set owner channel pinned = changed %v folder %d err %v, want changed in main folder", changed, folderID, err)
	}
	if changed, err := channels.ReorderChannelPinnedDialogs(ctx, owner.ID, domain.DialogMainFolderID, []domain.Peer{
		{Type: domain.PeerTypeChannel, ID: channelID},
	}, true); err != nil || changed {
		t.Fatalf("reorder owner channel pinned = changed %v err %v, want no-op", changed, err)
	}
	if changed, err := channels.SetChannelDialogUnreadMark(ctx, owner.ID, channelID, true); err != nil || !changed {
		t.Fatalf("set owner channel unread mark = changed %v err %v, want changed", changed, err)
	}
	ownerDialogs, err := channels.GetChannelDialogs(ctx, owner.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get owner channel dialogs before archive: %v", err)
	}
	if len(ownerDialogs.Dialogs) != 1 || !ownerDialogs.Dialogs[0].Pinned || ownerDialogs.Dialogs[0].PinnedOrder != 1 || !ownerDialogs.Dialogs[0].UnreadMark {
		t.Fatalf("owner channel dialog settings = %+v, want pinned order 1 with unread mark", ownerDialogs.Dialogs)
	}
	if err := channels.EditChannelPeerFolders(ctx, owner.ID, []domain.FolderPeerUpdate{
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}, FolderID: domain.DialogArchiveFolderID},
	}); err != nil {
		t.Fatalf("edit owner channel folder: %v", err)
	}
	ownerDialogs, err = channels.GetChannelDialogs(ctx, owner.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get owner channel dialogs after archive: %v", err)
	}
	// 归档清除 pinned（对齐 TDesktop History::setFolderPointer 的本地
	// unpin），unread_mark 与 folder_id 保留。
	if len(ownerDialogs.Dialogs) != 1 || ownerDialogs.Dialogs[0].Pinned || ownerDialogs.Dialogs[0].PinnedOrder != 0 || !ownerDialogs.Dialogs[0].UnreadMark || ownerDialogs.Dialogs[0].FolderID != domain.DialogArchiveFolderID {
		t.Fatalf("owner channel dialog settings = %+v, want unpinned after archive with unread mark kept", ownerDialogs.Dialogs)
	}
	unreadMarks, err := channels.ListChannelUnreadMarked(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list owner channel unread marks: %v", err)
	}
	if len(unreadMarks) != 1 || unreadMarks[0].ID != channelID || unreadMarks[0].Type != domain.PeerTypeChannel {
		t.Fatalf("channel unread marks = %+v, want channel", unreadMarks)
	}

	readers, err := channels.ListMessageReadParticipants(ctx, domain.ChannelReadParticipantsRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MessageID: sent.Message.ID,
		Limit:     domain.MaxChannelReadParticipants,
		Date:      1700000303,
	})
	if err != nil {
		t.Fatalf("list message read participants: %v", err)
	}
	if len(readers.Participants) != 1 || readers.Participants[0].UserID != friend.ID || readers.Participants[0].Date != 1700000302 {
		t.Fatalf("read participants = %+v, want friend read date", readers.Participants)
	}

	cleared, err := channels.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID:    friend.ID,
		ChannelID: channelID,
		MaxID:     sent.Message.ID,
		Date:      1700000302,
	})
	if err != nil {
		t.Fatalf("local clear history: %v", err)
	}
	if cleared.AvailableMinID != sent.Message.ID {
		t.Fatalf("local clear available_min_id = %d, want %d", cleared.AvailableMinID, sent.Message.ID)
	}
	staleClear, err := channels.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID:    friend.ID,
		ChannelID: channelID,
		MaxID:     created.Message.ID,
		Date:      1700000303,
	})
	if err != nil {
		t.Fatalf("stale local clear history: %v", err)
	}
	if staleClear.AvailableMinID != sent.Message.ID {
		t.Fatalf("stale local clear available_min_id = %d, want monotonic %d", staleClear.AvailableMinID, sent.Message.ID)
	}
	afterClear, err := channels.GetChannel(ctx, friend.ID, channelID)
	if err != nil {
		t.Fatalf("get channel after clear: %v", err)
	}
	if afterClear.Dialog.TopMessageID != 0 {
		t.Fatalf("dialog after clear = %+v, want no visible top", afterClear.Dialog)
	}

	next, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  902,
		Message:   "after local clear",
		Date:      1700000304,
	})
	if err != nil {
		t.Fatalf("send after clear: %v", err)
	}
	afterNext, err := channels.GetChannel(ctx, friend.ID, channelID)
	if err != nil {
		t.Fatalf("get channel after next: %v", err)
	}
	if afterNext.Dialog.TopMessageID != next.Message.ID || afterNext.Dialog.UnreadCount != 1 {
		t.Fatalf("dialog after next = %+v, want top %d unread 1", afterNext.Dialog, next.Message.ID)
	}
}

func TestChannelStoreStoryMessageForwardsPublicOnlyAndDeleteRollbackPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 914130,
		Phone:      "+1777" + suffix + "91",
		FirstName:  "StoryForwardOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channels := NewChannelStore(pool)
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) > 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})
	publicCreated, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "PG Story Forward Public " + suffix,
		Broadcast:     true,
		Date:          1700000330,
	})
	if err != nil {
		t.Fatalf("create public channel: %v", err)
	}
	channelIDs = append(channelIDs, publicCreated.Channel.ID)
	publicChannel, err := channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: publicCreated.Channel.ID,
		Username:  "storyfw" + suffix,
	})
	if err != nil {
		t.Fatalf("make public channel: %v", err)
	}
	privateCreated, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "PG Story Forward Private " + suffix,
		Broadcast:     true,
		Date:          1700000331,
	})
	if err != nil {
		t.Fatalf("create private channel: %v", err)
	}
	channelIDs = append(channelIDs, privateCreated.Channel.ID)
	source := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	media := &domain.MessageMedia{
		Kind: domain.MessageMediaKindStory,
		Story: &domain.MessageStory{
			Peer: source,
			ID:   71,
			Story: &domain.Story{
				Owner: source,
				ID:    71,
			},
		},
	}
	publicSent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: publicChannel.ID,
		RandomID:  9141301,
		Media:     media,
		Date:      1700000332,
	})
	if err != nil {
		t.Fatalf("send public story message: %v", err)
	}
	if _, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: privateCreated.Channel.ID,
		RandomID:  9141302,
		Media:     media,
		Date:      1700000333,
	}); err != nil {
		t.Fatalf("send private story message: %v", err)
	}
	list, err := channels.ListStoryMessageForwards(ctx, domain.StoryMessageForwardListRequest{
		ViewerUserID: owner.ID,
		Owner:        source,
		StoryID:      71,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list story message forwards: %v", err)
	}
	if list.Count != 1 || len(list.Forwards) != 1 {
		t.Fatalf("story message forwards = %+v, want one public forward", list)
	}
	if got := list.Forwards[0].PublicForward.Message.ChannelID; got != publicChannel.ID {
		t.Fatalf("forward channel = %d, want public channel %d", got, publicChannel.ID)
	}
	if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID:    owner.ID,
		ChannelID: publicChannel.ID,
		IDs:       []int{publicSent.Message.ID},
		Date:      1700000334,
	}); err != nil {
		t.Fatalf("delete public story message: %v", err)
	}
	empty, err := channels.ListStoryMessageForwards(ctx, domain.StoryMessageForwardListRequest{
		ViewerUserID: owner.ID,
		Owner:        source,
		StoryID:      71,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list story message forwards after delete: %v", err)
	}
	if empty.Count != 0 || len(empty.Forwards) != 0 {
		t.Fatalf("story message forwards after delete = %+v, want empty", empty)
	}
}

func TestChannelStoreSendMessageViaBotIDSurvivesReadPaths(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 3101, Phone: "+1888" + suffix + "01", FirstName: "InlineChannelOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 3102, Phone: "+1888" + suffix + "02", FirstName: "InlineChannelFriend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	bot, err := users.Create(ctx, domain.User{AccessHash: 3103, FirstName: "InlineViaBot", Username: "inline_via_pg_" + suffix + "_bot", Bot: true, BotInfoVersion: 1})
	if err != nil {
		t.Fatalf("create bot user: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID, bot.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Inline Via Channel " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID},
		Date:          1700001100,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	markup := &domain.MessageReplyMarkup{Inline: [][]domain.MarkupButton{{
		{Type: domain.MarkupButtonCallback, Text: "Open", Data: []byte{0x00, 0xff, 0x42}},
	}}}
	assertMarkup := func(name string, got *domain.MessageReplyMarkup) {
		t.Helper()
		if got == nil || len(got.Inline) != 1 || len(got.Inline[0]) != 1 {
			t.Fatalf("%s markup = %+v, want one callback button", name, got)
		}
		btn := got.Inline[0][0]
		if btn.Type != domain.MarkupButtonCallback || btn.Text != "Open" || !bytes.Equal(btn.Data, []byte{0x00, 0xff, 0x42}) {
			t.Fatalf("%s button = %+v, want callback Open bytes", name, btn)
		}
	}

	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:      owner.ID,
		ChannelID:   channelID,
		RandomID:    990301,
		Message:     "inline channel via",
		ViaBotID:    bot.ID,
		ReplyMarkup: markup,
		Date:        1700001101,
	})
	if err != nil {
		t.Fatalf("send channel via: %v", err)
	}
	if sent.Message.ViaBotID != bot.ID || sent.Event.Message.ViaBotID != bot.ID {
		t.Fatalf("sent via_bot_id = msg %d event %d, want %d", sent.Message.ViaBotID, sent.Event.Message.ViaBotID, bot.ID)
	}
	assertMarkup("sent message", sent.Message.ReplyMarkup)
	assertMarkup("sent event", sent.Event.Message.ReplyMarkup)

	duplicate, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  990301,
		Message:   "changed text must not win",
		Date:      1700001102,
	})
	if err != nil {
		t.Fatalf("duplicate channel via: %v", err)
	}
	if !duplicate.Duplicate || duplicate.Message.ViaBotID != bot.ID || duplicate.Event.Message.ViaBotID != bot.ID {
		t.Fatalf("duplicate = %+v event=%+v, want original via bot %d", duplicate.Message, duplicate.Event, bot.ID)
	}
	assertMarkup("duplicate message", duplicate.Message.ReplyMarkup)
	assertMarkup("duplicate event", duplicate.Event.Message.ReplyMarkup)

	history, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 10})
	if err != nil {
		t.Fatalf("history channel via: %v", err)
	}
	if len(history.Messages) == 0 || history.Messages[0].ViaBotID != bot.ID {
		t.Fatalf("history messages = %+v, want top via bot %d", history.Messages, bot.ID)
	}
	assertMarkup("history message", history.Messages[0].ReplyMarkup)

	byID, err := channels.GetChannelMessages(ctx, owner.ID, channelID, []int{sent.Message.ID})
	if err != nil {
		t.Fatalf("get channel messages via: %v", err)
	}
	if len(byID.Messages) != 1 || byID.Messages[0].ViaBotID != bot.ID {
		t.Fatalf("get messages = %+v, want via bot %d", byID.Messages, bot.ID)
	}
	assertMarkup("get messages", byID.Messages[0].ReplyMarkup)

	diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: owner.ID, ChannelID: channelID, Pts: created.Channel.Pts, Limit: 10})
	if err != nil {
		t.Fatalf("channel difference via: %v", err)
	}
	if len(diff.NewMessages) != 1 || diff.NewMessages[0].ViaBotID != bot.ID {
		t.Fatalf("difference messages = %+v, want via bot %d", diff.NewMessages, bot.ID)
	}
	assertMarkup("difference message", diff.NewMessages[0].ReplyMarkup)

	dialogs, err := channels.ListChannelDialogs(ctx, owner.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list dialogs via: %v", err)
	}
	if len(dialogs.Messages) == 0 || dialogs.Messages[0].ViaBotID != bot.ID {
		t.Fatalf("dialog top messages = %+v, want via bot %d", dialogs.Messages, bot.ID)
	}
	assertMarkup("dialog top message", dialogs.Messages[0].ReplyMarkup)

	editedMarkup := &domain.MessageReplyMarkup{Inline: [][]domain.MarkupButton{{
		{Type: domain.MarkupButtonCallback, Text: "Done", Data: []byte("v2")},
	}}}
	if _, err := channels.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID:          owner.ID,
		ChannelID:       channelID,
		ID:              sent.Message.ID,
		Message:         "wrong bot edit",
		ViaBotEditBotID: bot.ID + 1,
		EditDate:        1700001103,
		SetReplyMarkup:  true,
		ReplyMarkup:     editedMarkup,
	}); err != domain.ErrMessageAuthorRequired {
		t.Fatalf("wrong channel via bot edit err = %v, want ErrMessageAuthorRequired", err)
	}
	edited, err := channels.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID:          owner.ID,
		ChannelID:       channelID,
		ID:              sent.Message.ID,
		Message:         "inline channel edited",
		ViaBotEditBotID: bot.ID,
		EditDate:        1700001104,
		SetReplyMarkup:  true,
		ReplyMarkup:     editedMarkup,
	})
	if err != nil {
		t.Fatalf("channel via bot edit: %v", err)
	}
	if edited.Event.Type != domain.ChannelUpdateEditMessage || edited.Message.Body != "inline channel edited" || edited.Message.ViaBotID != bot.ID {
		t.Fatalf("edited = %+v event=%+v, want edit via bot", edited.Message, edited.Event)
	}
	if edited.Message.ReplyMarkup == nil || edited.Message.ReplyMarkup.Inline[0][0].Text != "Done" || !bytes.Equal(edited.Message.ReplyMarkup.Inline[0][0].Data, []byte("v2")) {
		t.Fatalf("edited markup = %+v, want Done/v2", edited.Message.ReplyMarkup)
	}
	editDiff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: owner.ID, ChannelID: channelID, Pts: sent.Event.Pts, Limit: 10})
	if err != nil {
		t.Fatalf("channel edit difference: %v", err)
	}
	if len(editDiff.OtherUpdates) != 1 || editDiff.OtherUpdates[0].Type != domain.ChannelUpdateEditMessage {
		t.Fatalf("channel edit difference = %+v, want one edit", editDiff.OtherUpdates)
	}
	if editDiff.OtherUpdates[0].Message.ReplyMarkup == nil || editDiff.OtherUpdates[0].Message.ReplyMarkup.Inline[0][0].Text != "Done" {
		t.Fatalf("channel edit difference markup = %+v, want Done", editDiff.OtherUpdates[0].Message.ReplyMarkup)
	}
}

func TestChannelStoreSendMessageSkipDeliveryAdvancesReadBoundary(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 731, Phone: "+1778" + suffix + "01", FirstName: "SkipOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 732, Phone: "+1778" + suffix + "02", FirstName: "SkipFriend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	bot, err := users.Create(ctx, domain.User{AccessHash: 733, Phone: "+1778" + suffix + "03", FirstName: "SkipBot", Bot: true, BotInfoVersion: 1})
	if err != nil {
		t.Fatalf("create bot-like user: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID, bot.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Skip Delivery " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID, bot.ID},
		Date:          1700000400,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	hidden, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:              owner.ID,
		ChannelID:           channelID,
		RandomID:            941,
		Message:             "hidden for privacy bot",
		SkipDeliveryUserIDs: []int64{bot.ID},
		Date:                1700000401,
	})
	if err != nil {
		t.Fatalf("send hidden: %v", err)
	}
	if containsInt64ForPostgresTest(hidden.Recipients, bot.ID) {
		t.Fatalf("hidden recipients = %+v, want bot skipped", hidden.Recipients)
	}

	// H4a：skip 成员的读边界推进只写 channel_members 行；unread 派生自动把被隐藏
	// 消息排除（read boundary 已越过它）。
	hiddenView, err := channels.GetChannelDialogs(ctx, bot.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get bot dialogs after hidden: %v", err)
	}
	if len(hiddenView.Dialogs) != 1 || hiddenView.Dialogs[0].UnreadCount != 0 ||
		hiddenView.Dialogs[0].ReadInboxMaxID != hidden.Message.ID {
		t.Fatalf("bot derived dialog after hidden = %+v, want unread 0 read %d", hiddenView.Dialogs, hidden.Message.ID)
	}
	var memberReadInbox int
	if err := pool.QueryRow(ctx, `
SELECT read_inbox_max_id
FROM channel_members
WHERE channel_id = $1 AND user_id = $2`, channelID, bot.ID).Scan(&memberReadInbox); err != nil {
		t.Fatalf("read bot member boundary: %v", err)
	}
	if memberReadInbox != hidden.Message.ID {
		t.Fatalf("bot member read_inbox = %d, want hidden message id %d", memberReadInbox, hidden.Message.ID)
	}

	visible, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  942,
		Message:   "/visible",
		Date:      1700000402,
	})
	if err != nil {
		t.Fatalf("send visible: %v", err)
	}
	if !containsInt64ForPostgresTest(visible.Recipients, bot.ID) {
		t.Fatalf("visible recipients = %+v, want bot", visible.Recipients)
	}
	visibleView, err := channels.GetChannelDialogs(ctx, bot.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get bot dialogs after visible: %v", err)
	}
	if len(visibleView.Dialogs) != 1 || visibleView.Dialogs[0].TopMessage != visible.Message.ID ||
		visibleView.Dialogs[0].ReadInboxMaxID != hidden.Message.ID || visibleView.Dialogs[0].UnreadCount != 1 {
		t.Fatalf("bot derived dialog after visible = %+v, want top %d read hidden %d unread visible-only",
			visibleView.Dialogs, visible.Message.ID, hidden.Message.ID)
	}
}

func containsInt64ForPostgresTest(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func TestChannelStoreConcurrentSendAndReadHistoryDoNotSurfaceDeadlock(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 37,
		Phone:      "+1777" + suffix + "21",
		FirstName:  "ConcurrentOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 38,
		Phone:      "+1777" + suffix + "22",
		FirstName:  "ConcurrentMember",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(context.Background(), "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Send Read Race " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000450,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	first, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  1700000451,
		Message:   "seed",
		Date:      1700000451,
	})
	if err != nil {
		t.Fatalf("seed send: %v", err)
	}

	for i := 0; i < 20; i++ {
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func(iter int) {
			defer wg.Done()
			<-start
			_, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
				UserID:    member.ID,
				ChannelID: channelID,
				RandomID:  int64(1700000500 + iter),
				Message:   fmt.Sprintf("race send %d", iter),
				Date:      1700000500 + iter,
			})
			errs <- err
		}(i)
		go func() {
			defer wg.Done()
			<-start
			_, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
				UserID:    member.ID,
				ChannelID: channelID,
				MaxID:     first.Message.ID,
				Date:      1700000600,
			})
			errs <- err
		}()
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent send/read iteration %d: %v", i, err)
			}
		}
	}
}

func TestChannelStoreJoinInitialReadWatermarkSkipsExistingHistory(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 131,
		Phone:      "+1777" + suffix + "11",
		FirstName:  "JoinOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{
		AccessHash: 132,
		Phone:      "+1777" + suffix + "12",
		FirstName:  "JoinFriend",
	})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Join Watermark " + suffix,
		Megagroup:     true,
		Date:          1700000320,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	first, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  905,
		Message:   "before join",
		Date:      1700000321,
	})
	if err != nil {
		t.Fatalf("send existing message: %v", err)
	}
	joined, err := channels.JoinChannel(ctx, channelID, friend.ID, 1700000322)
	if err != nil {
		t.Fatalf("join channel: %v", err)
	}
	if _, err := channels.JoinChannel(ctx, channelID, friend.ID, 1700000323); !errors.Is(err, domain.ErrUserAlreadyParticipant) {
		t.Fatalf("duplicate join err = %v, want ErrUserAlreadyParticipant", err)
	}
	if len(joined.Members) != 1 || joined.Members[0].ReadInboxMaxID != joined.Message.ID {
		t.Fatalf("joined member = %+v message=%+v, want read watermark at self join service", joined.Members, joined.Message)
	}
	view, err := channels.GetChannel(ctx, friend.ID, channelID)
	if err != nil {
		t.Fatalf("get joined channel: %v", err)
	}
	if view.Dialog.UnreadCount != 0 || view.Self.ReadInboxMaxID != joined.Message.ID {
		t.Fatalf("joined view dialog/self = %+v / %+v, want no unread and read at join service", view.Dialog, view.Self)
	}
	readers, err := channels.ListMessageReadParticipants(ctx, domain.ChannelReadParticipantsRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MessageID: first.Message.ID,
		Limit:     domain.MaxChannelReadParticipants,
		Date:      1700000323,
	})
	if err != nil {
		t.Fatalf("list read participants existing message: %v", err)
	}
	if len(readers.Participants) != 0 {
		t.Fatalf("existing message readers after join = %+v, want none from initial watermark", readers.Participants)
	}
	future, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  906,
		Message:   "after join",
		Date:      1700000323,
	})
	if err != nil {
		t.Fatalf("send future message: %v", err)
	}
	after, err := channels.GetChannel(ctx, friend.ID, channelID)
	if err != nil {
		t.Fatalf("get channel after future: %v", err)
	}
	if after.Dialog.TopMessageID != future.Message.ID || after.Dialog.UnreadCount != 1 {
		t.Fatalf("joined dialog after future = %+v, want top %d unread 1", after.Dialog, future.Message.ID)
	}
}

func TestChannelStoreInviteInitialReadWatermarkSkipsExistingHistory(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 41,
		Phone:      "+1888" + suffix + "01",
		FirstName:  "InviteOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	invited, err := users.Create(ctx, domain.User{
		AccessHash: 42,
		Phone:      "+1888" + suffix + "02",
		FirstName:  "InviteMember",
	})
	if err != nil {
		t.Fatalf("create invited: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, invited.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Invite Watermark " + suffix,
		Megagroup:     true,
		Date:          1700000320,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	first, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  902,
		Message:   "already visible before invite",
		Date:      1700000321,
	})
	if err != nil {
		t.Fatalf("send existing channel message: %v", err)
	}
	if _, err := channels.InviteToChannel(ctx, channelID, owner.ID, []int64{invited.ID}, 1700000322); err != nil {
		t.Fatalf("invite to channel: %v", err)
	}

	view, err := channels.GetChannel(ctx, invited.ID, channelID)
	if err != nil {
		t.Fatalf("get invited channel: %v", err)
	}
	if view.Self.ReadInboxMaxID != first.Message.ID || view.Dialog.ReadInboxMaxID != first.Message.ID {
		t.Fatalf("invited read watermark self/dialog = %d/%d, want existing top %d", view.Self.ReadInboxMaxID, view.Dialog.ReadInboxMaxID, first.Message.ID)
	}
	if view.Dialog.UnreadCount != 1 {
		t.Fatalf("invited unread = %d, want only invite service message unread", view.Dialog.UnreadCount)
	}
}

func TestChannelStoreImportInviteInitialReadWatermarkSkipsExistingHistory(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 51,
		Phone:      "+1888" + suffix + "11",
		FirstName:  "ImportOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	joiner, err := users.Create(ctx, domain.User{
		AccessHash: 52,
		Phone:      "+1888" + suffix + "12",
		FirstName:  "ImportJoiner",
	})
	if err != nil {
		t.Fatalf("create joiner: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, joiner.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Import Watermark " + suffix,
		Megagroup:     true,
		Date:          1700000330,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	first, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  912,
		Message:   "already visible before import",
		Date:      1700000331,
	})
	if err != nil {
		t.Fatalf("send existing channel message: %v", err)
	}
	invite, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		Title:     "join",
		Date:      1700000332,
	})
	if err != nil {
		t.Fatalf("export invite: %v", err)
	}
	joined, err := channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{
		UserID: joiner.ID,
		Hash:   invite.Invite.Hash,
		Date:   1700000333,
	})
	if err != nil {
		t.Fatalf("import invite: %v", err)
	}
	if len(joined.Members) != 1 || joined.Members[0].ReadInboxMaxID != joined.Message.ID || joined.Members[0].ReadOutboxMaxID != joined.Message.ID {
		t.Fatalf("imported member = %+v message=%+v, want read watermarks at self join service", joined.Members, joined.Message)
	}
	view, err := channels.GetChannel(ctx, joiner.ID, channelID)
	if err != nil {
		t.Fatalf("get imported channel: %v", err)
	}
	if view.Dialog.UnreadCount != 0 || view.Self.ReadInboxMaxID != joined.Message.ID {
		t.Fatalf("imported view dialog/self = %+v / %+v, want no unread and read at join service", view.Dialog, view.Self)
	}
	readers, err := channels.ListMessageReadParticipants(ctx, domain.ChannelReadParticipantsRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MessageID: first.Message.ID,
		Limit:     domain.MaxChannelReadParticipants,
		Date:      1700000334,
	})
	if err != nil {
		t.Fatalf("list read participants existing message: %v", err)
	}
	if len(readers.Participants) != 0 {
		t.Fatalf("existing message readers after import = %+v, want none from initial watermark", readers.Participants)
	}
}

func TestChannelStoreSendMessageResolvesReplyTopID(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 41,
		Phone:      "+1778" + suffix + "01",
		FirstName:  "ReplyOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{
		AccessHash: 42,
		Phone:      "+1778" + suffix + "02",
		FirstName:  "ReplyFriend",
	})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Reply Top " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID},
		Date:          1700000350,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	root, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  911,
		Message:   "root",
		Date:      1700000351,
	})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	reply, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    friend.ID,
		ChannelID: channelID,
		RandomID:  912,
		Message:   "reply",
		ReplyTo:   &domain.MessageReply{MessageID: root.Message.ID, QuoteText: "root"},
		Date:      1700000352,
	})
	if err != nil {
		t.Fatalf("send reply: %v", err)
	}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}
	if reply.Message.ReplyTo == nil || reply.Message.ReplyTo.Peer != channelPeer || reply.Message.ReplyTo.TopMessageID != root.Message.ID {
		t.Fatalf("reply metadata = %+v, want channel peer and top id %d", reply.Message.ReplyTo, root.Message.ID)
	}
	nested, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  913,
		Message:   "nested",
		ReplyTo:   &domain.MessageReply{MessageID: reply.Message.ID},
		Date:      1700000353,
	})
	if err != nil {
		t.Fatalf("send nested reply: %v", err)
	}
	if nested.Message.ReplyTo == nil || nested.Message.ReplyTo.TopMessageID != root.Message.ID {
		t.Fatalf("nested reply metadata = %+v, want inherited top id %d", nested.Message.ReplyTo, root.Message.ID)
	}
	_, err = channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  914,
		Message:   "bad quote offset",
		ReplyTo: &domain.MessageReply{
			MessageID:   root.Message.ID,
			QuoteText:   "root",
			QuoteOffset: domain.MaxMessageReplyQuoteOffset + 1,
		},
		Date: 1700000354,
	})
	if !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
		t.Fatalf("bad quote offset err = %v, want ErrReplyMessageIDInvalid", err)
	}
}

func TestChannelStoreHistorySupportsOffsetDateOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 37,
		Phone:      "+1778" + suffix + "03",
		FirstName:  "HistoryDateOwner",
	})
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
		Title:         "History Date " + suffix,
		Megagroup:     true,
		Date:          1700000360,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	if _, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  921,
		Message:   "old",
		Date:      1700000361,
	}); err != nil {
		t.Fatalf("send old: %v", err)
	}
	if _, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  922,
		Message:   "new",
		Date:      1700000362,
	}); err != nil {
		t.Fatalf("send new: %v", err)
	}

	history, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{
		ChannelID:  channelID,
		OffsetDate: 1700000362,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("list channel history: %v", err)
	}
	if len(history.Messages) != 2 || history.Messages[0].Body != "old" || history.Messages[1].Action == nil {
		t.Fatalf("history = %+v, want only messages older than offset date", history.Messages)
	}
}

func TestChannelStoreDeleteHistoryForEveryoneBatchesHugeMaxID(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 71,
		Phone:      "+1998" + suffix + "01",
		FirstName:  "BulkChannelOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{
		AccessHash: 72,
		Phone:      "+1998" + suffix + "02",
		FirstName:  "BulkChannelFriend",
	})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Bulk Delete " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID},
		Date:          1700000600,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	total := domain.MaxDeleteHistoryBatch + 2
	if _, err := pool.Exec(ctx, `
WITH src AS (
  SELECT generate_series(2, $3::int + 1) AS id
),
msgs AS (
  INSERT INTO channel_messages (
    channel_id,
    id,
    random_id,
    sender_user_id,
    from_peer_type,
    from_peer_id,
    message_date,
    body,
    entities,
    pts
  )
  SELECT
    $1::bigint,
    id,
    920000000 + id,
    $2::bigint,
    'user',
    $2::bigint,
    1700000600 + id,
    'bulk channel history',
    '[]'::jsonb,
    id
  FROM src
  RETURNING id, message_date
)
INSERT INTO channel_update_events (
  channel_id,
  pts,
  pts_count,
  date,
  event_type,
  message_id,
  sender_user_id,
  payload
)
SELECT
  $1::bigint,
  id,
  1,
  message_date,
  'new_channel_message',
  id,
  $2::bigint,
  '{}'::jsonb
FROM msgs
`, channelID, owner.ID, total); err != nil {
		t.Fatalf("seed bulk channel messages: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE channels
SET top_message_id = $2,
    pts = $2,
    updated_at = now()
WHERE id = $1`, channelID, total+1); err != nil {
		t.Fatalf("update channel bulk top: %v", err)
	}

	first, err := channels.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID:      owner.ID,
		ChannelID:   channelID,
		MaxID:       int(^uint(0) >> 1),
		ForEveryone: true,
		Date:        1700000700,
	})
	if err != nil {
		t.Fatalf("DeleteChannelHistory first batch: %v", err)
	}
	wantFirstPts := total + 1 + domain.MaxDeleteHistoryBatch
	if first.Offset != 1 || first.Event.Pts != wantFirstPts || first.Event.PtsCount != domain.MaxDeleteHistoryBatch || len(first.DeletedIDs) != domain.MaxDeleteHistoryBatch {
		t.Fatalf("first batch = %+v, want offset=1 pts=%d pts_count=%d", first, wantFirstPts, domain.MaxDeleteHistoryBatch)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_messages WHERE channel_id = $1 AND NOT deleted`, channelID).Scan(&remaining); err != nil {
		t.Fatalf("count remaining after first batch: %v", err)
	}
	if remaining != 3 {
		t.Fatalf("remaining after first batch = %d, want create service + two oldest messages", remaining)
	}

	second, err := channels.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID:      owner.ID,
		ChannelID:   channelID,
		MaxID:       int(^uint(0) >> 1),
		ForEveryone: true,
		Date:        1700000701,
	})
	if err != nil {
		t.Fatalf("DeleteChannelHistory second batch: %v", err)
	}
	if second.Offset != 0 || second.Event.Pts != wantFirstPts+2 || second.Event.PtsCount != 2 || len(second.DeletedIDs) != 2 {
		t.Fatalf("second batch = %+v, want final offset=0 pts=%d pts_count=2 keeping create service message", second, wantFirstPts+2)
	}
	if second.Channel.TopMessageID != 1 {
		t.Fatalf("top after full clear = %d, want create service message 1", second.Channel.TopMessageID)
	}
	var keptID int
	if err := pool.QueryRow(ctx, `SELECT id FROM channel_messages WHERE channel_id = $1 AND NOT deleted`, channelID).Scan(&keptID); err != nil {
		t.Fatalf("query kept message after full clear: %v", err)
	}
	if keptID != 1 {
		t.Fatalf("kept message id = %d, want create service message 1", keptID)
	}
	var dialogTops []int
	rows, err := pool.Query(ctx, `SELECT top_message_id FROM channel_dialogs WHERE channel_id = $1 ORDER BY user_id`, channelID)
	if err != nil {
		t.Fatalf("query member dialogs after full clear: %v", err)
	}
	for rows.Next() {
		var top int
		if err := rows.Scan(&top); err != nil {
			rows.Close()
			t.Fatalf("scan member dialog top: %v", err)
		}
		dialogTops = append(dialogTops, top)
	}
	rows.Close()
	if len(dialogTops) != 2 || dialogTops[0] != 1 || dialogTops[1] != 1 {
		t.Fatalf("member dialog tops after full clear = %v, want create service message visible for both members", dialogTops)
	}
}
