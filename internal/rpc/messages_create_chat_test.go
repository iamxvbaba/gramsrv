package rpc

import (
	"context"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appprivacy "telesrv/internal/app/privacy"
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

func TestMessagesCreateChatFiltersPrivacyBeforeMembershipWritesRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550001031", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	allowed, err := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550001032", FirstName: "Allowed"})
	if err != nil {
		t.Fatalf("create allowed user: %v", err)
	}
	denied, err := userStore.Create(ctx, domain.User{AccessHash: 33, Phone: "15550001033", FirstName: "Denied"})
	if err != nil {
		t.Fatalf("create denied user: %v", err)
	}
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), memory.NewContactStore())
	if _, err := privacy.SetRules(ctx, denied.ID, domain.PrivacyKeyChatInvite, []domain.PrivacyRule{
		{Kind: domain.PrivacyRuleDisallowAll},
	}); err != nil {
		t.Fatalf("set denied invite privacy: %v", err)
	}
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
		Privacy:  privacy,
	}, zaptest.NewLogger(t), clock.System)

	invited, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{
			&tg.InputUser{UserID: allowed.ID, AccessHash: allowed.AccessHash},
			&tg.InputUser{UserID: denied.ID, AccessHash: denied.AccessHash},
		},
		Title: "Privacy Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	if len(invited.MissingInvitees) != 1 || invited.MissingInvitees[0].UserID != denied.ID {
		t.Fatalf("missing invitees = %+v, want denied user %d", invited.MissingInvitees, denied.ID)
	}
	updates, ok := invited.Updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", invited.Updates)
	}
	var channel *tg.Channel
	for _, chat := range updates.Chats {
		if candidate, ok := chat.(*tg.Channel); ok {
			channel = candidate
			break
		}
	}
	if channel == nil || channel.ParticipantsCount != 2 {
		t.Fatalf("channel = %#v, want owner + allowed only", channel)
	}
	for _, update := range updates.Updates {
		newMessage, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		service, ok := newMessage.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		add, ok := service.Action.(*tg.MessageActionChatAddUser)
		if !ok {
			continue
		}
		if len(add.Users) != 1 || add.Users[0] != allowed.ID {
			t.Fatalf("invite action users = %v, want allowed user %d only", add.Users, allowed.ID)
		}
	}

	participants, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get participants: %v", err)
	}
	list := participants.(*tg.ChannelsChannelParticipants)
	if list.Count != 2 {
		t.Fatalf("participant count = %d, want 2", list.Count)
	}
	for _, user := range list.Users {
		if got, ok := user.(*tg.User); ok && got.ID == denied.ID {
			t.Fatalf("denied user %d was written as a member", denied.ID)
		}
	}
}

func TestMessagesCreateChatCreatesOwnerOnlyMegagroupRPC(t *testing.T) {
	tests := []struct {
		name  string
		phone string
		users func(domain.User) []tg.InputUserClass
	}{
		{
			name:  "empty vector",
			phone: "15550001021",
			users: func(domain.User) []tg.InputUserClass { return nil },
		},
		{
			name:  "self references normalize to empty",
			phone: "15550001022",
			users: func(owner domain.User) []tg.InputUserClass {
				return []tg.InputUserClass{
					&tg.InputUserSelf{},
					&tg.InputUser{UserID: owner.ID, AccessHash: owner.AccessHash},
					&tg.InputUserSelf{},
				}
			},
		},
	}

	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			userStore := memory.NewUserStore()
			owner, err := userStore.Create(ctx, domain.User{AccessHash: int64(21 + index), Phone: tc.phone, FirstName: "Owner"})
			if err != nil {
				t.Fatalf("create owner: %v", err)
			}
			channelStore := memory.NewChannelStore()
			channels := appchannels.NewService(channelStore)
			sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
			r := New(Config{}, Deps{
				Users:    appusers.NewService(userStore),
				Channels: channels,
				Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
				Sessions: sessions,
			}, zaptest.NewLogger(t), clock.System)

			authKeyID := [8]byte{0x60, byte(index + 1)}
			sessionID := int64(70 + index)
			requestCtx := WithClientInfo(
				WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), authKeyID), sessionID),
				ClientInfo{DeviceModel: "Android", AppVersion: "12.7.3"},
			)
			invited, err := r.onMessagesCreateChat(requestCtx, &tg.MessagesCreateChatRequest{
				Users: tc.users(owner),
				Title: "Owner Only Group",
			})
			if err != nil {
				t.Fatalf("create owner-only chat: %v", err)
			}
			if len(invited.MissingInvitees) != 0 {
				t.Fatalf("missing invitees = %+v, want empty", invited.MissingInvitees)
			}

			updates, ok := invited.Updates.(*tg.Updates)
			if !ok || len(updates.Chats) != 2 {
				t.Fatalf("updates = %T %+v, want legacy chat + channel", invited.Updates, invited.Updates)
			}
			legacy, ok := updates.Chats[0].(*tg.Chat)
			if !ok || !legacy.Deactivated || !legacy.Creator || legacy.ParticipantsCount != 1 {
				t.Fatalf("legacy chat = %#v, want migrated creator-only chat", updates.Chats[0])
			}
			channel, ok := updates.Chats[1].(*tg.Channel)
			if !ok || !channel.Megagroup || channel.Broadcast || !channel.Creator || channel.ParticipantsCount != 1 {
				t.Fatalf("channel = %#v, want owner-only megagroup", updates.Chats[1])
			}
			migrated, ok := legacy.GetMigratedTo()
			if !ok {
				t.Fatal("legacy chat missing migrated_to")
			}
			migratedChannel, ok := migrated.(*tg.InputChannel)
			if !ok || migratedChannel.ChannelID != channel.ID || migratedChannel.AccessHash != channel.AccessHash {
				t.Fatalf("migrated_to = %#v, want channel %d/%d", migrated, channel.ID, channel.AccessHash)
			}
			if len(updates.Updates) != 2 {
				t.Fatalf("updates len = %d, want create service message + channel refresh only", len(updates.Updates))
			}
			created, ok := updates.Updates[0].(*tg.UpdateNewChannelMessage)
			if !ok || created.Pts != 1 || created.PtsCount != 1 {
				t.Fatalf("create update = %#v, want pts=1/count=1", updates.Updates[0])
			}
			createdMessage, ok := created.Message.(*tg.MessageService)
			if !ok {
				t.Fatalf("create message = %T, want messageService", created.Message)
			}
			if _, ok := createdMessage.Action.(*tg.MessageActionChannelCreate); !ok {
				t.Fatalf("create action = %T, want messageActionChannelCreate", createdMessage.Action)
			}
			if refresh, ok := updates.Updates[1].(*tg.UpdateChannel); !ok || refresh.ChannelID != channel.ID {
				t.Fatalf("refresh = %#v, want channel %d", updates.Updates[1], channel.ID)
			}
			if len(updates.Users) != 1 {
				t.Fatalf("updates users len = %d, want creator only", len(updates.Users))
			}
			if user, ok := updates.Users[0].(*tg.User); !ok || user.ID != owner.ID {
				t.Fatalf("updates user = %#v, want owner %d", updates.Users[0], owner.ID)
			}

			pushedUserIDs := sessions.pushedUserIDs()
			if len(pushedUserIDs) != 1 || pushedUserIDs[0] != owner.ID {
				t.Fatalf("push user ids = %v, want creator's other sessions", pushedUserIDs)
			}
			push := sessions.snapshot()
			if push.sessionID != sessionID || sessions.scopedAuthKey() != authKeyID {
				t.Fatalf("push exclusion = auth_key %x session %d, want %x/%d", sessions.scopedAuthKey(), push.sessionID, authKeyID, sessionID)
			}
			canonicalPush, ok := sessions.userMessage.(*tg.Updates)
			if !ok || len(canonicalPush.Chats) != 1 {
				t.Fatalf("creator push = %T %+v, want canonical channel updates", sessions.userMessage, sessions.userMessage)
			}
			if pushedChannel, ok := canonicalPush.Chats[0].(*tg.Channel); !ok || pushedChannel.ID != channel.ID {
				t.Fatalf("creator pushed chat = %#v, want channel %d", canonicalPush.Chats[0], channel.ID)
			}

			participants, err := r.onChannelsGetParticipants(requestCtx, &tg.ChannelsGetParticipantsRequest{
				Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
				Filter:  &tg.ChannelParticipantsRecent{},
				Limit:   10,
			})
			if err != nil {
				t.Fatalf("get participants: %v", err)
			}
			participantList, ok := participants.(*tg.ChannelsChannelParticipants)
			if !ok || participantList.Count != 1 || len(participantList.Participants) != 1 || len(participantList.Users) != 1 {
				t.Fatalf("participants = %T %+v, want creator only", participants, participants)
			}
			if creator, ok := participantList.Participants[0].(*tg.ChannelParticipantCreator); !ok || creator.UserID != owner.ID {
				t.Fatalf("participant = %#v, want creator %d", participantList.Participants[0], owner.ID)
			}

			view, err := channels.GetChannel(ctx, owner.ID, channel.ID)
			if err != nil {
				t.Fatalf("get created channel: %v", err)
			}
			if view.Self.Role != domain.ChannelRoleCreator || view.Self.Status != domain.ChannelMemberActive {
				t.Fatalf("self membership = %+v, want active creator", view.Self)
			}
			if view.Dialog.TopMessageID != createdMessage.ID || view.Dialog.ReadInboxMaxID != createdMessage.ID || view.Dialog.UnreadCount != 0 {
				t.Fatalf("creator dialog = %+v, want creation message %d read", view.Dialog, createdMessage.ID)
			}

			var dialogsBuffer bin.Buffer
			if err := (&tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}).Encode(&dialogsBuffer); err != nil {
				t.Fatalf("encode getDialogs: %v", err)
			}
			dialogsResult, err := r.Dispatch(requestCtx, authKeyID, sessionID, &dialogsBuffer)
			if err != nil {
				t.Fatalf("dispatch getDialogs: %v", err)
			}
			dialogs, ok := dialogsResult.(*tg.MessagesDialogs)
			if !ok || len(dialogs.Dialogs) != 1 || len(dialogs.Chats) != 1 || len(dialogs.Messages) != 1 {
				t.Fatalf("dialogs = %T %+v, want persisted owner-only group", dialogsResult, dialogsResult)
			}

			var historyBuffer bin.Buffer
			if err := (&tg.MessagesGetHistoryRequest{
				Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
				Limit: 20,
			}).Encode(&historyBuffer); err != nil {
				t.Fatalf("encode getHistory: %v", err)
			}
			historyResult, err := r.Dispatch(requestCtx, authKeyID, sessionID, &historyBuffer)
			if err != nil {
				t.Fatalf("dispatch getHistory: %v", err)
			}
			history, ok := historyResult.(*tg.MessagesChannelMessages)
			if !ok || len(history.Messages) != 1 {
				t.Fatalf("history = %T %+v, want creation service message", historyResult, historyResult)
			}
			if message, ok := history.Messages[0].(*tg.MessageService); !ok || message.ID != createdMessage.ID {
				t.Fatalf("history message = %#v, want creation service %d", history.Messages[0], createdMessage.ID)
			}

			difference, err := r.onUpdatesGetChannelDifference(requestCtx, &tg.UpdatesGetChannelDifferenceRequest{
				Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
				Filter:  &tg.ChannelMessagesFilterEmpty{},
				Pts:     0,
				Limit:   10,
			})
			if err != nil {
				t.Fatalf("getChannelDifference from pts=0: %v", err)
			}
			fullDifference, ok := difference.(*tg.UpdatesChannelDifference)
			if !ok || fullDifference.Pts != 1 || len(fullDifference.NewMessages) != 1 {
				t.Fatalf("difference = %T %+v, want creation event at pts=1", difference, difference)
			}
		})
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
	if len(pushUserIDs) != 2 || pushUserIDs[0] != owner.ID || pushUserIDs[1] != friend.ID {
		t.Fatalf("push user ids = %v, want creator then invited friend %d/%d", pushUserIDs, owner.ID, friend.ID)
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
