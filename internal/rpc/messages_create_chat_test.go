package rpc

import (
	"context"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strings"
	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestMessagesCreateChatCreatesMegagroupAndDialogsRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550001002", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	invited, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "E2E Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	updates, ok := invited.Updates.(*tg.Updates)
	if !ok || len(updates.Chats) != 1 {
		t.Fatalf("updates = %T %+v, want one chat", invited.Updates, invited.Updates)
	}
	channel, ok := updates.Chats[0].(*tg.Channel)
	if !ok || !channel.Megagroup || channel.Broadcast {
		t.Fatalf("chat = %#v, want megagroup channel", updates.Chats[0])
	}
	assertDefaultBannedRightsAllowsSend(t, channel)
	if len(updates.Updates) != 4 {
		t.Fatalf("updates len = %d, want create/invite service messages plus channel refreshes", len(updates.Updates))
	}
	newMsg, ok := updates.Updates[0].(*tg.UpdateNewChannelMessage)
	if !ok || newMsg.Pts != 1 || newMsg.PtsCount != 1 {
		t.Fatalf("create update = %#v, want channel pts=1", updates.Updates[0])
	}
	if refresh, ok := updates.Updates[1].(*tg.UpdateChannel); !ok || refresh.ChannelID != channel.ID {
		t.Fatalf("create refresh = %#v, want channel refresh", updates.Updates[1])
	}
	service, ok := newMsg.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("create message = %T, want service", newMsg.Message)
	}
	if _, ok := service.Action.(*tg.MessageActionChannelCreate); !ok {
		t.Fatalf("service action = %T, want channel create", service.Action)
	}
	inviteMsg, ok := updates.Updates[2].(*tg.UpdateNewChannelMessage)
	if !ok || inviteMsg.Pts != 2 || inviteMsg.PtsCount != 1 {
		t.Fatalf("invite update = %#v, want channel pts=2", updates.Updates[2])
	}
	if refresh, ok := updates.Updates[3].(*tg.UpdateChannel); !ok || refresh.ChannelID != channel.ID {
		t.Fatalf("invite refresh = %#v, want channel refresh", updates.Updates[3])
	}
	inviteService, ok := inviteMsg.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("invite message = %T, want service", inviteMsg.Message)
	}
	addUser, ok := inviteService.Action.(*tg.MessageActionChatAddUser)
	if !ok || len(addUser.Users) != 1 || addUser.Users[0] != friend.ID {
		t.Fatalf("invite action = %#v, want add friend %d", inviteService.Action, friend.ID)
	}
	if len(updates.Users) != 2 {
		t.Fatalf("updates users len = %d, want owner + friend", len(updates.Users))
	}

	participants, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get participants: %v", err)
	}
	participantList, ok := participants.(*tg.ChannelsChannelParticipants)
	if !ok || participantList.Count != 2 || len(participantList.Participants) != 2 || len(participantList.Users) != 2 {
		t.Fatalf("participants = %T %+v, want owner + friend participants/users", participants, participants)
	}

	req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}
	var b bin.Buffer
	if err := req.Encode(&b); err != nil {
		t.Fatalf("encode get dialogs: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &b)
	if err != nil {
		t.Fatalf("dispatch get dialogs: %v", err)
	}
	dialogs, ok := enc.(*tg.MessagesDialogs)
	if !ok {
		t.Fatalf("dialogs response = %T, want *tg.MessagesDialogs", enc)
	}
	if len(dialogs.Dialogs) != 1 || len(dialogs.Chats) != 1 || len(dialogs.Messages) != 1 {
		t.Fatalf("dialogs = %+v, want channel dialog/chat/message", dialogs)
	}
	dialog := dialogs.Dialogs[0].(*tg.Dialog)
	if peer, ok := dialog.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != channel.ID {
		t.Fatalf("dialog peer = %#v, want channel %d", dialog.Peer, channel.ID)
	}
}

func TestMessagesCreateChatRejectsEmptyInviteListRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 21, Phone: "15550001021", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(memory.NewChannelStore()),
	}, zaptest.NewLogger(t), clock.System)

	if _, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Title: "No Invitees",
	}); err == nil || !strings.Contains(err.Error(), "USERS_TOO_FEW") {
		t.Fatalf("create chat without users err = %v, want USERS_TOO_FEW", err)
	}
}

func TestMessagesCreateChatTDesktopReturnsLegacyChatAndAcceptsInputPeerChatRPC(t *testing.T) {
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
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)
	tdCtx := WithClientInfo(WithUserID(ctx, owner.ID), ClientInfo{
		DeviceModel: "Desktop",
		AppVersion:  "6.8.4 x64",
		LangPack:    "tdesktop",
	})

	invited, err := r.onMessagesCreateChat(tdCtx, &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "TDesktop Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	updates, ok := invited.Updates.(*tg.Updates)
	if !ok || len(updates.Chats) != 2 {
		t.Fatalf("updates = %T %+v, want legacy chat + channel", invited.Updates, invited.Updates)
	}
	legacy, ok := updates.Chats[0].(*tg.Chat)
	if !ok || !legacy.Deactivated {
		t.Fatalf("legacy chat = %#v, want migrated chat", updates.Chats[0])
	}
	assertDefaultBannedRightsAllowsSend(t, legacy)
	channel, ok := updates.Chats[1].(*tg.Channel)
	if !ok || !channel.Megagroup || channel.Broadcast {
		t.Fatalf("channel = %#v, want megagroup channel", updates.Chats[1])
	}
	assertDefaultBannedRightsAllowsSend(t, channel)
	if !channel.Creator {
		t.Fatalf("channel creator flag = false, want true for creator")
	}
	if rights, ok := channel.GetAdminRights(); !ok || !rights.ChangeInfo || !rights.InviteUsers {
		t.Fatalf("channel admin rights = %+v ok=%v, want creator manage rights", rights, ok)
	}
	migrated, ok := legacy.GetMigratedTo()
	if !ok {
		t.Fatalf("legacy chat missing migrated_to")
	}
	migratedTo, ok := migrated.(*tg.InputChannel)
	if !ok || migratedTo.ChannelID != channel.ID || migratedTo.AccessHash != channel.AccessHash {
		t.Fatalf("migrated_to = %#v, want channel %d/%d", migrated, channel.ID, channel.AccessHash)
	}

	participants, err := r.onChannelsGetParticipants(tdCtx, &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get participants after create chat: %v", err)
	}
	participantList, ok := participants.(*tg.ChannelsChannelParticipants)
	if !ok || len(participantList.Chats) != 0 {
		t.Fatalf("participants = %T %+v, want no chat side vector", participants, participants)
	}
	if participantList.Count != 2 || len(participantList.Participants) != 2 || len(participantList.Users) != 2 {
		t.Fatalf("participants count/rows/users = %d/%d/%d, want owner + friend",
			participantList.Count, len(participantList.Participants), len(participantList.Users))
	}
	legacyHistoryReq := &tg.MessagesGetHistoryRequest{
		Peer:  &tg.InputPeerChat{ChatID: channel.ID},
		Limit: 20,
	}
	var legacyHistoryBuf bin.Buffer
	if err := legacyHistoryReq.Encode(&legacyHistoryBuf); err != nil {
		t.Fatalf("encode legacy history: %v", err)
	}
	legacyHistory, err := r.Dispatch(tdCtx, [8]byte{}, 0, &legacyHistoryBuf)
	if err != nil {
		t.Fatalf("legacy history: %v", err)
	}
	legacyMessages, ok := legacyHistory.(*tg.MessagesMessages)
	if !ok {
		t.Fatalf("legacy history = %T %+v, want *tg.MessagesMessages", legacyHistory, legacyHistory)
	}
	if len(legacyMessages.Messages) != 0 {
		t.Fatalf("legacy history = %T %+v, want empty messages.messages", legacyHistory, legacyHistory)
	}

	sent, err := r.onMessagesSendMessage(tdCtx, &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChat{ChatID: channel.ID},
		RandomID: 99,
		Message:  "via legacy input peer",
	})
	if err != nil {
		t.Fatalf("send via inputPeerChat: %v", err)
	}
	sentUpdates, ok := sent.(*tg.Updates)
	if !ok || len(sentUpdates.Updates) < 2 {
		t.Fatalf("send updates = %T %+v, want channel message updates", sent, sent)
	}
	newMsg, ok := sentUpdates.Updates[1].(*tg.UpdateNewChannelMessage)
	if !ok {
		t.Fatalf("send update = %#v, want updateNewChannelMessage", sentUpdates.Updates[1])
	}
	msg, ok := newMsg.Message.(*tg.Message)
	if !ok || msg.Message != "via legacy input peer" {
		t.Fatalf("sent message = %#v, want text channel message", newMsg.Message)
	}
	if peer, ok := msg.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != channel.ID {
		t.Fatalf("sent peer = %#v, want peerChannel %d", msg.PeerID, channel.ID)
	}
}

func TestMessagesCreateChatDispatchRemembersTDesktopClientInfo(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550001041", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550001042", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	sessions := &captureSessions{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	rawAuthKeyID := [8]byte{0x42, 0x01}
	sessionID := int64(77)
	initReq := &tg.InvokeWithLayerRequest{
		Layer: 225,
		Query: &tg.InitConnectionRequest{
			APIID:          111111,
			DeviceModel:    "Desktop",
			AppVersion:     "6.8.4 x64",
			SystemLangCode: "en",
			LangPack:       "tdesktop",
			LangCode:       "en",
			Query:          &tg.HelpGetConfigRequest{},
		},
	}
	var initBuf bin.Buffer
	if err := initReq.Encode(&initBuf); err != nil {
		t.Fatalf("encode init: %v", err)
	}
	if _, err := r.Dispatch(WithUserID(ctx, owner.ID), rawAuthKeyID, sessionID, &initBuf); err != nil {
		t.Fatalf("dispatch init: %v", err)
	}

	createReq := &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Dispatch TDesktop Group",
	}
	var createBuf bin.Buffer
	if err := createReq.Encode(&createBuf); err != nil {
		t.Fatalf("encode create chat: %v", err)
	}
	enc, err := r.Dispatch(context.Background(), rawAuthKeyID, sessionID, &createBuf)
	if err != nil {
		t.Fatalf("dispatch create chat: %v", err)
	}
	invited, ok := enc.(*tg.MessagesInvitedUsers)
	if !ok {
		t.Fatalf("create response = %T, want messages.invitedUsers", enc)
	}
	updates, ok := invited.Updates.(*tg.Updates)
	if !ok || len(updates.Chats) != 2 {
		t.Fatalf("updates = %T %+v, want legacy chat + channel", invited.Updates, invited.Updates)
	}
	if legacy, ok := updates.Chats[0].(*tg.Chat); !ok || !legacy.Deactivated {
		t.Fatalf("first chat = %#v, want migrated legacy chat", updates.Chats[0])
	}
	channel, ok := updates.Chats[1].(*tg.Channel)
	if !ok || !channel.Megagroup || !channel.Creator {
		t.Fatalf("second chat = %#v, want creator megagroup channel", updates.Chats[1])
	}
	if len(updates.Updates) != 4 {
		t.Fatalf("updates len = %d, want create/invite service messages plus channel refreshes", len(updates.Updates))
	}
	participants, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get participants: %v", err)
	}
	list, ok := participants.(*tg.ChannelsChannelParticipants)
	if !ok || list.Count != 2 || len(list.Participants) != 2 || len(list.Users) != 2 {
		t.Fatalf("participants = %T %+v, want owner + friend", participants, participants)
	}
	sessions.mu.Lock()
	pushUserIDs := append([]int64(nil), sessions.pushUserIDs...)
	sessions.mu.Unlock()
	if len(pushUserIDs) != 1 || pushUserIDs[0] != friend.ID {
		t.Fatalf("push user ids = %v, want only invited friend %d", pushUserIDs, friend.ID)
	}
}

func TestMessagesCreateChatSessionWithoutClientInfoReturnsLegacyChat(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550001051", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 52, Phone: "15550001052", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	invited, err := r.onMessagesCreateChat(WithSessionID(WithUserID(ctx, owner.ID), 99), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Session Legacy Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	updates, ok := invited.Updates.(*tg.Updates)
	if !ok || len(updates.Chats) != 2 {
		t.Fatalf("updates = %T %+v, want legacy chat + channel", invited.Updates, invited.Updates)
	}
	if legacy, ok := updates.Chats[0].(*tg.Chat); !ok || !legacy.Deactivated {
		t.Fatalf("first chat = %#v, want migrated legacy chat", updates.Chats[0])
	}
	if channel, ok := updates.Chats[1].(*tg.Channel); !ok || !channel.Megagroup || !channel.Creator {
		t.Fatalf("second chat = %#v, want creator megagroup channel", updates.Chats[1])
	}
}
