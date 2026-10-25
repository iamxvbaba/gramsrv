package rpc

import (
	"context"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestPublicChannelPreviewRPCsAllowNonMember(t *testing.T) {
	ctx := context.Background()
	sessions := &captureSessions{}
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 92001, Phone: "15550092001", FirstName: "Owner"})
	viewer, _ := userStore.Create(ctx, domain.User{AccessHash: 92002, Phone: "15550092002", FirstName: "Viewer"})
	channelStore := memory.NewChannelStore()
	deliveryOutbox := memory.NewDeliveryOutboxStore()
	channelStore.AttachDeliveryOutbox(deliveryOutbox)
	channelService := appchannels.NewService(channelStore)
	dialogService := appdialogs.NewService(memory.NewDialogStore(), channelStore)
	r := New(Config{}, Deps{
		Users:          appusers.NewService(userStore),
		Channels:       channelService,
		Dialogs:        dialogService,
		Sessions:       sessions,
		DeliveryOutbox: deliveryOutbox,
	}, zaptest.NewLogger(t), clock.System)
	public, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Public Preview RPC",
		Broadcast: true,
		Date:      1700010100,
	})
	if err != nil {
		t.Fatalf("create public channel: %v", err)
	}
	if _, err := channelService.UpdateUsername(ctx, owner.ID, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: public.Channel.ID,
		Username:  "public_preview_rpc",
	}); err != nil {
		t.Fatalf("publish channel username: %v", err)
	}
	sent, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: public.Channel.ID,
		RandomID:  201,
		Message:   "public preview rpc post",
		Date:      1700010110,
	})
	if err != nil {
		t.Fatalf("send public post: %v", err)
	}
	input := &tg.InputChannel{ChannelID: public.Channel.ID, AccessHash: public.Channel.AccessHash}
	peer := &tg.InputPeerChannel{ChannelID: public.Channel.ID, AccessHash: public.Channel.AccessHash}

	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, viewer.ID), input)
	if err != nil {
		t.Fatalf("non-member getFullChannel public preview: %v", err)
	}
	if len(full.Chats) != 1 {
		t.Fatalf("full chats = %d, want one channel", len(full.Chats))
	}
	chat, ok := full.Chats[0].(*tg.Channel)
	if !ok || !chat.Left || chat.ID != public.Channel.ID {
		t.Fatalf("full channel chat = %T %+v, want left public channel", full.Chats[0], full.Chats[0])
	}
	channelFull, ok := full.FullChat.(*tg.ChannelFull)
	if !ok || channelFull.ID != public.Channel.ID || channelFull.UnreadCount != 0 {
		t.Fatalf("full chat = %T %+v, want channel full without unread", full.FullChat, full.FullChat)
	}

	chats, err := r.onChannelsGetChannels(WithUserID(ctx, viewer.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("non-member getChannels public preview: %v", err)
	}
	if len(chats.(*tg.MessagesChats).Chats) != 1 {
		t.Fatalf("getChannels chats = %d, want one public preview channel", len(chats.(*tg.MessagesChats).Chats))
	}
	listed, ok := chats.(*tg.MessagesChats).Chats[0].(*tg.Channel)
	if !ok || !listed.Left || listed.ID != public.Channel.ID {
		t.Fatalf("getChannels chat = %T %+v, want left public channel", chats.(*tg.MessagesChats).Chats[0], chats.(*tg.MessagesChats).Chats[0])
	}

	sendAs, err := r.onChannelsGetSendAs(WithUserID(ctx, viewer.ID), &tg.ChannelsGetSendAsRequest{Peer: peer})
	if err != nil {
		t.Fatalf("non-member getSendAs public preview: %v", err)
	}
	if len(sendAs.Peers) != 0 {
		t.Fatalf("sendAs peers = %+v, non-member cannot post to broadcast", sendAs.Peers)
	}
	if len(sendAs.Chats) != 1 {
		t.Fatalf("sendAs chats = %d, want public channel chat", len(sendAs.Chats))
	}

	historyReq := &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 10}
	var in bin.Buffer
	if err := historyReq.Encode(&in); err != nil {
		t.Fatalf("encode getHistory: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, viewer.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch getHistory public preview: %v", err)
	}
	history, ok := enc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("getHistory response = %T, want *tg.MessagesChannelMessages", enc)
	}
	foundPost := false
	for _, item := range history.Messages {
		if msg, ok := item.(*tg.Message); ok && msg.Message == "public preview rpc post" {
			foundPost = true
		}
	}
	if !foundPost {
		t.Fatalf("history messages = %+v, want public preview post", history.Messages)
	}
	if len(history.Chats) != 1 {
		t.Fatalf("history chats = %d, want public channel chat", len(history.Chats))
	}
	historyChat, ok := history.Chats[0].(*tg.Channel)
	if !ok || !historyChat.Left || historyChat.ID != public.Channel.ID {
		t.Fatalf("history chat = %T %+v, want left public channel", history.Chats[0], history.Chats[0])
	}
	readOK, err := r.onChannelsReadHistory(WithUserID(ctx, viewer.ID), &tg.ChannelsReadHistoryRequest{
		Channel: input,
		MaxID:   sent.Message.ID,
	})
	if err != nil || !readOK {
		t.Fatalf("non-member readHistory public preview = %v err=%v, want successful no-op", readOK, err)
	}

	viewerCtx := WithSessionID(WithRawAuthKeyID(WithUserID(ctx, viewer.ID), [8]byte{9, 2}), 9202)
	diff, err := r.onUpdatesGetChannelDifference(viewerCtx, &tg.UpdatesGetChannelDifferenceRequest{
		Channel: input,
		Pts:     public.Event.Pts,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("non-member getChannelDifference public preview: %v", err)
	}
	fullDiff, ok := diff.(*tg.UpdatesChannelDifference)
	if !ok || !fullDiff.Final || fullDiff.Pts != sent.Event.Pts || len(fullDiff.NewMessages) != 1 {
		t.Fatalf("channel difference = %T %+v, want one public preview message", diff, diff)
	}
	message, ok := fullDiff.NewMessages[0].(*tg.Message)
	if !ok || message.ID != sent.Message.ID || message.Message != sent.Message.Body {
		t.Fatalf("channel difference message = %T %+v, want sent public post", fullDiff.NewMessages[0], fullDiff.NewMessages[0])
	}
	if subscribers := sessions.OnlineChannelSubscriberUserIDs(public.Channel.ID, 10); len(subscribers) != 1 || subscribers[0] != viewer.ID {
		t.Fatalf("public channel subscribers = %v, want viewer %d", subscribers, viewer.ID)
	}

	live, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: public.Channel.ID,
		RandomID:  202,
		Message:   "public preview live post",
		Date:      1700010120,
	})
	if err != nil {
		t.Fatalf("send live public post: %v", err)
	}
	sessions.clearMessages()
	if pushed := sessions.pushedUserIDs(); len(pushed) != 0 {
		t.Fatalf("Core pushed durable public-channel update directly: %v", pushed)
	}

	domainPeers, err := r.dialogPeersFromInput(WithUserID(ctx, viewer.ID), viewer.ID, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: peer}})
	if err != nil {
		t.Fatalf("dialog peer conversion public preview: %v", err)
	}
	if len(domainPeers) != 1 || domainPeers[0].Type != domain.PeerTypeChannel || domainPeers[0].ID != public.Channel.ID {
		t.Fatalf("domain peers = %+v, want public channel peer", domainPeers)
	}
	directPeerDialogs, err := dialogService.GetPeerDialogs(ctx, viewer.ID, domainPeers)
	if err != nil {
		t.Fatalf("dialog service public preview: %v", err)
	}
	if len(directPeerDialogs.Dialogs) != 1 || len(directPeerDialogs.ChannelMessages) != 0 || len(directPeerDialogs.Channels) != 1 {
		t.Fatalf("direct peer dialogs = %+v, want one zero-top public preview bootstrap", directPeerDialogs)
	}
	directDialog := directPeerDialogs.Dialogs[0]
	if directDialog.TopMessage != 0 || !directDialog.ChannelLeft || directDialog.Pts != live.Event.Pts {
		t.Fatalf("direct public preview dialog = %+v, want left zero-top bootstrap", directDialog)
	}

	peerDialogsReq := &tg.MessagesGetPeerDialogsRequest{
		Peers: []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: peer}},
	}
	var peerDialogsIn bin.Buffer
	if err := peerDialogsReq.Encode(&peerDialogsIn); err != nil {
		t.Fatalf("encode getPeerDialogs: %v", err)
	}
	peerDialogsEnc, err := r.Dispatch(WithUserID(ctx, viewer.ID), [8]byte{}, 0, &peerDialogsIn)
	if err != nil {
		t.Fatalf("dispatch getPeerDialogs public preview: %v", err)
	}
	peerDialogs, ok := peerDialogsEnc.(*tg.MessagesPeerDialogs)
	if !ok {
		t.Fatalf("getPeerDialogs response = %T, want peer dialogs", peerDialogsEnc)
	}
	if len(peerDialogs.Dialogs) != 1 || len(peerDialogs.Messages) != 0 || len(peerDialogs.Chats) != 1 {
		t.Fatalf("peer dialogs = %+v, want one zero-top public preview bootstrap", peerDialogs)
	}
	tgDialog, ok := peerDialogs.Dialogs[0].(*tg.Dialog)
	if !ok || tgDialog.TopMessage != 0 || tgDialog.ReadInboxMaxID != 0 ||
		tgDialog.ReadOutboxMaxID != 0 || tgDialog.UnreadCount != 0 {
		t.Fatalf("peer dialog = %T %+v, want zero-state dialog", peerDialogs.Dialogs[0], peerDialogs.Dialogs[0])
	}
	peerDialogChat, ok := peerDialogs.Chats[0].(*tg.Channel)
	if !ok || !peerDialogChat.Left || peerDialogChat.ID != public.Channel.ID {
		t.Fatalf("peer dialog chat = %T %+v, want left public channel", peerDialogs.Chats[0], peerDialogs.Chats[0])
	}

	if _, err := channelService.JoinChannel(ctx, viewer.ID, public.Channel.ID, 1700010120, testPendingJoinEffects); err != nil {
		t.Fatalf("join public channel after preview: %v", err)
	}
	var joinedPeerDialogsIn bin.Buffer
	if err := peerDialogsReq.Encode(&joinedPeerDialogsIn); err != nil {
		t.Fatalf("encode joined getPeerDialogs: %v", err)
	}
	joinedPeerDialogsEnc, err := r.Dispatch(WithUserID(ctx, viewer.ID), [8]byte{}, 0, &joinedPeerDialogsIn)
	if err != nil {
		t.Fatalf("dispatch getPeerDialogs after join: %v", err)
	}
	joinedPeerDialogs, ok := joinedPeerDialogsEnc.(*tg.MessagesPeerDialogs)
	if !ok {
		t.Fatalf("joined getPeerDialogs response = %T, want peer dialogs", joinedPeerDialogsEnc)
	}
	if len(joinedPeerDialogs.Chats) != 1 {
		t.Fatalf("joined peer dialog chats = %d, want one channel", len(joinedPeerDialogs.Chats))
	}
	joinedChat, ok := joinedPeerDialogs.Chats[0].(*tg.Channel)
	if !ok || joinedChat.Left || joinedChat.ID != public.Channel.ID {
		t.Fatalf("joined peer dialog chat = %T %+v, want active channel with left=false", joinedPeerDialogs.Chats[0], joinedPeerDialogs.Chats[0])
	}
}

func TestChannelsReadHistoryAllowsSyntheticMonoforumViewers(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 92101, Phone: "15550092101", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	subscriber, err := userStore.Create(ctx, domain.User{AccessHash: 92102, Phone: "15550092102", FirstName: "Subscriber"})
	if err != nil {
		t.Fatal(err)
	}
	channelStore := memory.NewChannelStore()
	deliveryOutbox := memory.NewDeliveryOutboxStore()
	channelStore.AttachDeliveryOutbox(deliveryOutbox)
	channels := appchannels.NewService(channelStore)
	parent, err := channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title: "Monoforum Read RPC", Broadcast: true, Date: 1_700_011_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := channelStore.SetPaidMessagesPrice(ctx, owner.ID, parent.Channel.ID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	mono, err := channelStore.GetChannelByID(ctx, enabled.Channel.LinkedMonoforumID)
	if err != nil {
		t.Fatal(err)
	}
	r := New(Config{}, Deps{
		Users: appusers.NewService(userStore), Channels: channels, DeliveryOutbox: deliveryOutbox,
	}, zaptest.NewLogger(t), clock.System)
	for _, userID := range []int64{owner.ID, subscriber.ID} {
		ok, err := r.onChannelsReadHistory(WithUserID(ctx, userID), &tg.ChannelsReadHistoryRequest{
			Channel: &tg.InputChannel{ChannelID: mono.ID, AccessHash: mono.AccessHash},
			MaxID:   mono.TopMessageID,
		})
		if err != nil || !ok {
			t.Fatalf("synthetic monoforum read for %d = %v err=%v, want successful no-op", userID, ok, err)
		}
	}
}
