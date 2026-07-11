package rpc

import (
	"context"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"
	"strconv"
	"strings"
	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

type countingDiscussionReadChannels struct {
	ChannelsService
	delegate           *appchannels.Service
	resolveCalls       int
	getDiscussionCalls int
}

type emptyDiscussionBotProfiles struct{}

func (emptyDiscussionBotProfiles) BotInfo(context.Context, int64) (domain.BotProfile, bool, error) {
	return domain.BotProfile{}, false, nil
}

func TestLinkedDiscussionGuestCanCommentWithoutMembership(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, _ := users.Create(ctx, domain.User{AccessHash: 301, Phone: "15550003001", FirstName: "Owner"})
	subscriber, _ := users.Create(ctx, domain.User{AccessHash: 302, Phone: "15550003002", FirstName: "Subscriber"})
	channelStore := memory.NewChannelStore()
	channels := appchannels.NewService(channelStore, appchannels.WithBotProfileResolver(emptyDiscussionBotProfiles{}))
	r := New(Config{}, Deps{Users: appusers.NewService(users), Channels: channels}, zaptest.NewLogger(t), clock.System)

	broadcast, err := channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "Private channel", Broadcast: true, Date: 1700003001})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	group, err := channels.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{Title: "Comments", Date: 1700003002})
	if err != nil {
		t.Fatalf("create discussion group: %v", err)
	}
	if _, err := channels.InviteToChannel(ctx, owner.ID, broadcast.Channel.ID, []int64{subscriber.ID}, 1700003003); err != nil {
		t.Fatalf("invite broadcast subscriber: %v", err)
	}
	inputBroadcast := &tg.InputChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash}
	inputGroup := &tg.InputChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash}
	if ok, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, owner.ID), &tg.ChannelsSetDiscussionGroupRequest{Broadcast: inputBroadcast, Group: inputGroup}); err != nil || !ok {
		t.Fatalf("link discussion group = %v, %v", ok, err)
	}
	postUpdates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer: &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash}, Message: "post", RandomID: 3001,
	})
	if err != nil {
		t.Fatalf("send post: %v", err)
	}
	post := postUpdates.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	discussion, err := r.onMessagesGetDiscussionMessage(WithUserID(ctx, subscriber.ID), &tg.MessagesGetDiscussionMessageRequest{
		Peer: &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash}, MsgID: post.ID,
	})
	if err != nil || len(discussion.Messages) != 1 {
		t.Fatalf("get discussion as subscriber: messages=%d err=%v", len(discussion.Messages), err)
	}
	root := discussion.Messages[0].(*tg.Message)
	req := &tg.MessagesSendMessageRequest{
		Peer: &tg.InputPeerChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash}, Message: "guest comment", RandomID: 3002,
	}
	req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: root.ID})
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, subscriber.ID), req); err != nil {
		t.Fatalf("send linked guest comment: %v", err)
	}
	if _, err := r.onChannelsGetFullChannel(WithUserID(ctx, subscriber.ID), inputGroup); err != nil {
		t.Fatalf("get linked group full as guest: %v", err)
	}
	participant, err := r.onChannelsGetParticipant(WithUserID(ctx, subscriber.ID), &tg.ChannelsGetParticipantRequest{
		Channel: inputGroup, Participant: &tg.InputPeerSelf{},
	})
	if err != nil {
		t.Fatalf("get linked guest participant: %v", err)
	}
	if _, ok := participant.Participant.(*tg.ChannelParticipantLeft); !ok {
		t.Fatalf("linked guest participant = %T, want channelParticipantLeft", participant.Participant)
	}
	if _, err := r.onChannelsGetParticipants(WithUserID(ctx, subscriber.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: inputGroup, Filter: &tg.ChannelParticipantsRecent{}, Limit: 20,
	}); err != nil {
		t.Fatalf("get linked group participants as guest: %v", err)
	}
	if _, err := r.onChannelsGetParticipants(WithUserID(ctx, subscriber.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: inputGroup, Filter: &tg.ChannelParticipantsBots{}, Limit: 20,
	}); err != nil {
		t.Fatalf("get linked group bot participants as guest: %v", err)
	}
	if _, err := r.onMessagesGetReplies(WithUserID(ctx, subscriber.ID), &tg.MessagesGetRepliesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash}, MsgID: root.ID, Limit: 20,
	}); err != nil {
		t.Fatalf("get linked group replies as guest: %v", err)
	}
	if changed, err := r.onMessagesReadDiscussion(WithUserID(ctx, subscriber.ID), &tg.MessagesReadDiscussionRequest{
		Peer: &tg.InputPeerChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash}, MsgID: root.ID, ReadMaxID: group.Channel.TopMessageID,
	}); err != nil || changed {
		t.Fatalf("guest read discussion = changed %v err %v, want authorized no-op", changed, err)
	}
	guestView, err := channels.GetChannel(ctx, subscriber.ID, group.Channel.ID)
	if err != nil || !guestView.Self.Guest || guestView.Self.Status != domain.ChannelMemberLeft {
		t.Fatalf("guest view after send = %+v err %v, want non-persisted left guest", guestView.Self, err)
	}
	if _, err := channels.SetJoinToSend(ctx, owner.ID, group.Channel.ID, true); err != nil {
		t.Fatalf("enable join_to_send: %v", err)
	}
	req.RandomID = 3003
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, subscriber.ID), req); err == nil || !strings.Contains(err.Error(), "CHAT_WRITE_FORBIDDEN") {
		t.Fatalf("guest send with join_to_send err = %v, want CHAT_WRITE_FORBIDDEN", err)
	}
}

func (s *countingDiscussionReadChannels) ResolveDiscussionReadTarget(ctx context.Context, userID, sourceChannelID int64, sourceMessageID, readMaxID int) (domain.ChannelDiscussionReadTarget, error) {
	s.resolveCalls++
	return s.delegate.ResolveDiscussionReadTarget(ctx, userID, sourceChannelID, sourceMessageID, readMaxID)
}

func (s *countingDiscussionReadChannels) GetLinkedDiscussionChannel(ctx context.Context, userID, sourceChannelID int64) (domain.ChannelView, error) {
	return s.delegate.GetLinkedDiscussionChannel(ctx, userID, sourceChannelID)
}

func (s *countingDiscussionReadChannels) GetDiscussionMessage(ctx context.Context, userID, channelID int64, msgID int) (domain.ChannelDiscussionMessage, error) {
	s.getDiscussionCalls++
	return s.delegate.GetDiscussionMessage(ctx, userID, channelID, msgID)
}

func TestChannelDialogCarriesChannelPts(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550001151", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Pts Group",
		Megagroup: true,
		Date:      1000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sent, err := channelSvc.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  7001,
		Message:   "pts probe",
		Date:      1100,
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	wantPts := sent.Event.Pts
	if wantPts <= 0 {
		t.Fatalf("sent channel pts = %d, want > 0", wantPts)
	}

	dispatch := func(req bin.Encoder) bin.Encoder {
		t.Helper()
		var b bin.Buffer
		if err := req.Encode(&b); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		enc, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &b)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		return enc
	}

	got := dispatch(&tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20})
	box, ok := got.(*tg.MessagesDialogsBox)
	if !ok {
		t.Fatalf("dialogs response = %T, want box", got)
	}
	dialogs, ok := box.Dialogs.(*tg.MessagesDialogs)
	if !ok || len(dialogs.Dialogs) != 1 {
		t.Fatalf("dialogs = %T %+v, want one channel dialog", box.Dialogs, box.Dialogs)
	}
	dialog, ok := dialogs.Dialogs[0].(*tg.Dialog)
	if !ok {
		t.Fatalf("dialog = %T, want *tg.Dialog", dialogs.Dialogs[0])
	}
	pts, ok := dialog.GetPts()
	if !ok || pts != wantPts {
		t.Fatalf("dialog pts = %d (set=%v), want %d: clients seed channel difference state from dialog.pts", pts, ok, wantPts)
	}

	peerResp := dispatch(&tg.MessagesGetPeerDialogsRequest{
		Peers: []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: &tg.InputPeerChannel{
			ChannelID:  created.Channel.ID,
			AccessHash: created.Channel.AccessHash,
		}}},
	})
	peerDialogs, ok := peerResp.(*tg.MessagesPeerDialogs)
	if !ok || len(peerDialogs.Dialogs) != 1 {
		t.Fatalf("peer dialogs = %T %+v, want one dialog", peerResp, peerResp)
	}
	peerDialog, ok := peerDialogs.Dialogs[0].(*tg.Dialog)
	if !ok {
		t.Fatalf("peer dialog = %T, want *tg.Dialog", peerDialogs.Dialogs[0])
	}
	if pts, ok := peerDialog.GetPts(); !ok || pts != wantPts {
		t.Fatalf("peer dialog pts = %d (set=%v), want %d", pts, ok, wantPts)
	}
}

func TestChannelsGetInactiveChannelsReturnsLeastActiveRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550001041", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	heir, err := userStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550001042", FirstName: "Heir"})
	if err != nil {
		t.Fatalf("create heir: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	createAndSend := func(title string, createDate, msgDate int, memberIDs ...int64) int64 {
		t.Helper()
		created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
			Title:         title,
			Megagroup:     true,
			MemberUserIDs: memberIDs,
			Date:          createDate,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		if _, err := channelSvc.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
			ChannelID: created.Channel.ID,
			RandomID:  int64(msgDate),
			Message:   title + " message",
			Date:      msgDate,
		}); err != nil {
			t.Fatalf("send %s: %v", title, err)
		}
		return created.Channel.ID
	}

	oldID := createAndSend("Old inactive", 1000, 1100)
	midID := createAndSend("Middle inactive", 1000, 1200)
	newID := createAndSend("New inactive", 1000, 1300)
	leftID := createAndSend("Left inactive", 1000, 900, heir.ID)
	if _, err := channelSvc.LeaveChannel(ctx, owner.ID, leftID, 1400); err != nil {
		t.Fatalf("leave channel: %v", err)
	}

	got, err := r.onChannelsGetInactiveChannels(WithUserID(ctx, owner.ID))
	if err != nil {
		t.Fatalf("get inactive channels: %v", err)
	}
	if len(got.Dates) != 3 || len(got.Chats) != 3 || len(got.Users) != 0 {
		t.Fatalf("inactive chats = %+v, want three active channel chats and no users", got)
	}
	wantIDs := []int64{oldID, midID, newID}
	wantDates := []int{1100, 1200, 1300}
	for i, chat := range got.Chats {
		channel, ok := chat.(*tg.Channel)
		if !ok {
			t.Fatalf("chat %d = %T, want *tg.Channel", i, chat)
		}
		if channel.ID != wantIDs[i] || got.Dates[i] != wantDates[i] {
			t.Fatalf("inactive item %d = id %d date %d, want id %d date %d", i, channel.ID, got.Dates[i], wantIDs[i], wantDates[i])
		}
	}
}

func TestChannelsGetChannelRecommendationsRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550001051", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := userStore.Create(ctx, domain.User{AccessHash: 52, Phone: "15550001052", FirstName: "Other"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	createPublicBroadcast := func(creator domain.User, title, username string, date int) domain.Channel {
		t.Helper()
		created, err := channelSvc.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
			Title:     title,
			Broadcast: true,
			Date:      date,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		channel, err := channelSvc.UpdateUsername(ctx, creator.ID, domain.UpdateChannelUsernameRequest{
			ChannelID: created.Channel.ID,
			Username:  username,
		})
		if err != nil {
			t.Fatalf("set username for %s: %v", title, err)
		}
		return channel
	}

	source := createPublicBroadcast(owner, "Source Recommendations", "source_recs", 1000)
	for i := 0; i < 12; i++ {
		createPublicBroadcast(owner, "Candidate "+strconv.Itoa(i), "rec"+strconv.Itoa(i)+"public", 2000+i)
	}
	groupCreated, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Public Group",
		Megagroup: true,
		Date:      3000,
	})
	if err != nil {
		t.Fatalf("create public group: %v", err)
	}
	group, err := channelSvc.UpdateUsername(ctx, owner.ID, domain.UpdateChannelUsernameRequest{
		ChannelID: groupCreated.Channel.ID,
		Username:  "group_recs",
	})
	if err != nil {
		t.Fatalf("set group username: %v", err)
	}

	recommendationsReq := func(channel domain.Channel) *tg.ChannelsGetChannelRecommendationsRequest {
		req := &tg.ChannelsGetChannelRecommendationsRequest{}
		req.SetChannel(&tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
		return req
	}

	got, err := r.onChannelsGetChannelRecommendations(WithUserID(ctx, owner.ID), recommendationsReq(source))
	if err != nil {
		t.Fatalf("get recommendations by source: %v", err)
	}
	slice, ok := got.(*tg.MessagesChatsSlice)
	if !ok {
		t.Fatalf("recommendations = %T %+v, want messages.chatsSlice", got, got)
	}
	if slice.Count != 12 || len(slice.Chats) != domain.DefaultChannelRecommendationsLimit {
		t.Fatalf("recommendations count=%d len=%d, want count 12 len %d", slice.Count, len(slice.Chats), domain.DefaultChannelRecommendationsLimit)
	}
	for _, chat := range slice.Chats {
		channel, ok := chat.(*tg.Channel)
		if !ok {
			t.Fatalf("recommendation chat = %T, want channel", chat)
		}
		if channel.ID == source.ID || !channel.Broadcast || channel.Megagroup || channel.Username == "" {
			t.Fatalf("recommendation channel = %+v, want public broadcast excluding source", channel)
		}
	}

	if _, err := r.onChannelsGetChannelRecommendations(WithUserID(ctx, owner.ID), recommendationsReq(group)); err == nil || !strings.Contains(err.Error(), "CHANNEL_INVALID") {
		t.Fatalf("megagroup recommendations err = %v, want CHANNEL_INVALID", err)
	}

	globalA := createPublicBroadcast(other, "Global A", "global_recs_a", 5000)
	globalB := createPublicBroadcast(other, "Global B", "global_recs_b", 5100)
	global, err := r.onChannelsGetChannelRecommendations(WithUserID(ctx, owner.ID), &tg.ChannelsGetChannelRecommendationsRequest{})
	if err != nil {
		t.Fatalf("get global recommendations: %v", err)
	}
	box, ok := global.(*tg.MessagesChats)
	if !ok {
		t.Fatalf("global recommendations = %T %+v, want messages.chats", global, global)
	}
	if len(box.Chats) != 2 {
		t.Fatalf("global recommendations len=%d chats=%+v, want two channels", len(box.Chats), box.Chats)
	}
	wantIDs := []int64{globalB.ID, globalA.ID}
	for i, chat := range box.Chats {
		channel, ok := chat.(*tg.Channel)
		if !ok {
			t.Fatalf("global chat %d = %T, want channel", i, chat)
		}
		if channel.ID != wantIDs[i] {
			t.Fatalf("global chat %d id=%d, want %d", i, channel.ID, wantIDs[i])
		}
	}
}

func TestChannelsGetLeftChannelsRPCReturnsLeftFlagAndSafePaging(t *testing.T) {
	ctx := context.Background()
	const (
		ownerID  int64 = 1000000901
		memberID int64 = 1000000902
	)
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{Channels: channelService}, zaptest.NewLogger(t), clock.System)
	older, err := channelService.CreateMegagroupFromCreateChat(ctx, ownerID, domain.CreateChannelRequest{
		Title:         "Older Left",
		MemberUserIDs: []int64{memberID},
		Date:          1700000900,
	})
	if err != nil {
		t.Fatalf("create older megagroup: %v", err)
	}
	newer, err := channelService.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:         "Newer Left Broadcast",
		Broadcast:     true,
		MemberUserIDs: []int64{memberID},
		Date:          1700000901,
	})
	if err != nil {
		t.Fatalf("create newer broadcast: %v", err)
	}
	if _, err := channelService.CreateMegagroupFromCreateChat(ctx, ownerID, domain.CreateChannelRequest{
		Title:         "Active Excluded",
		MemberUserIDs: []int64{memberID},
		Date:          1700000902,
	}); err != nil {
		t.Fatalf("create active megagroup: %v", err)
	}
	if _, err := channelService.LeaveChannel(ctx, memberID, older.Channel.ID, 1700000903); err != nil {
		t.Fatalf("leave older channel: %v", err)
	}
	if _, err := channelService.LeaveChannel(ctx, memberID, newer.Channel.ID, 1700000904); err != nil {
		t.Fatalf("leave newer channel: %v", err)
	}

	got, err := r.onChannelsGetLeftChannels(WithUserID(ctx, memberID), 0)
	if err != nil {
		t.Fatalf("get left channels: %v", err)
	}
	chats, ok := got.(*tg.MessagesChats)
	if !ok || len(chats.Chats) != 2 {
		t.Fatalf("left channels = %T %+v, want final messages.chats with two chats", got, got)
	}
	first, ok := chats.Chats[0].(*tg.Channel)
	if !ok || first.ID != newer.Channel.ID || !first.Left {
		t.Fatalf("first left channel = %+v (%T), want newest with left flag", chats.Chats[0], chats.Chats[0])
	}
	second, ok := chats.Chats[1].(*tg.Channel)
	if !ok || second.ID != older.Channel.ID || !second.Left {
		t.Fatalf("second left channel = %+v (%T), want older with left flag", chats.Chats[1], chats.Chats[1])
	}

	empty, err := r.onChannelsGetLeftChannels(WithUserID(ctx, memberID), 2)
	if err != nil {
		t.Fatalf("get empty left channels page: %v", err)
	}
	emptySlice, ok := empty.(*tg.MessagesChatsSlice)
	if !ok || emptySlice.Count != 2 || len(emptySlice.Chats) != 0 {
		t.Fatalf("empty left page = %T %+v, want empty slice with full count", empty, empty)
	}
	if _, err := r.onChannelsGetLeftChannels(WithUserID(ctx, memberID), domain.MaxLeftChannelsOffset+1); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("huge offset err = %v, want LIMIT_INVALID", err)
	}
}

func TestChannelsDiscussionGroupRPCPersistsFullChannelLink(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1000000911
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{Channels: channelService}, zaptest.NewLogger(t), clock.System)
	broadcast, err := channelService.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Discussion Broadcast",
		Broadcast: true,
		Date:      1700000910,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	group, err := channelService.CreateMegagroupFromCreateChat(ctx, ownerID, domain.CreateChannelRequest{
		Title: "Discussion Group",
		Date:  1700000911,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	inputBroadcast := &tg.InputChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash}
	inputGroup := &tg.InputChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash}

	candidates, err := r.onChannelsGetGroupsForDiscussion(WithUserID(ctx, ownerID))
	if err != nil {
		t.Fatalf("get groups for discussion: %v", err)
	}
	candidateChats := candidates.(*tg.MessagesChats).Chats
	if len(candidateChats) != 1 || candidateChats[0].(*tg.Channel).ID != group.Channel.ID {
		t.Fatalf("discussion candidates = %+v, want the creator megagroup", candidateChats)
	}
	if ok, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, ownerID), &tg.ChannelsSetDiscussionGroupRequest{
		Broadcast: inputBroadcast,
		Group:     inputGroup,
	}); err != nil || !ok {
		t.Fatalf("set discussion group = ok %v err %v, want true", ok, err)
	}
	fullBroadcast, err := r.onChannelsGetFullChannel(WithUserID(ctx, ownerID), inputBroadcast)
	if err != nil {
		t.Fatalf("get full broadcast: %v", err)
	}
	linkedID, ok := fullBroadcast.FullChat.(*tg.ChannelFull).GetLinkedChatID()
	if !ok || linkedID != group.Channel.ID {
		t.Fatalf("broadcast linked_chat_id = %d ok %v, want group %d", linkedID, ok, group.Channel.ID)
	}
	fullGroup, err := r.onChannelsGetFullChannel(WithUserID(ctx, ownerID), inputGroup)
	if err != nil {
		t.Fatalf("get full group: %v", err)
	}
	groupLinkedID, ok := fullGroup.FullChat.(*tg.ChannelFull).GetLinkedChatID()
	if !ok || groupLinkedID != broadcast.Channel.ID {
		t.Fatalf("group linked_chat_id = %d ok %v, want broadcast %d", groupLinkedID, ok, broadcast.Channel.ID)
	}
	gotChannel := fullBroadcast.Chats[0].(*tg.Channel)
	if !gotChannel.GetHasLink() {
		t.Fatalf("broadcast channel = %+v, want has_link", gotChannel)
	}
	if ok, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, ownerID), &tg.ChannelsSetDiscussionGroupRequest{
		Broadcast: &tg.InputChannelEmpty{},
		Group:     inputGroup,
	}); err != nil || !ok {
		t.Fatalf("unlink discussion group from group side = ok %v err %v, want true", ok, err)
	}
	fullBroadcast, err = r.onChannelsGetFullChannel(WithUserID(ctx, ownerID), inputBroadcast)
	if err != nil {
		t.Fatalf("get full broadcast after unlink: %v", err)
	}
	if linkedID, ok := fullBroadcast.FullChat.(*tg.ChannelFull).GetLinkedChatID(); ok || linkedID != 0 {
		t.Fatalf("broadcast linked_chat_id after unlink = %d ok %v, want unset", linkedID, ok)
	}
	if _, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, ownerID), &tg.ChannelsSetDiscussionGroupRequest{
		Broadcast: &tg.InputChannelEmpty{},
		Group:     inputGroup,
	}); err == nil || !strings.Contains(err.Error(), "LINK_NOT_MODIFIED") {
		t.Fatalf("repeat unlink err = %v, want LINK_NOT_MODIFIED", err)
	}
	if _, err := channelService.SetPreHistoryHidden(ctx, ownerID, group.Channel.ID, true); err != nil {
		t.Fatalf("hide group prehistory: %v", err)
	}
	if _, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, ownerID), &tg.ChannelsSetDiscussionGroupRequest{
		Broadcast: inputBroadcast,
		Group:     inputGroup,
	}); err == nil || !strings.Contains(err.Error(), "MEGAGROUP_PREHISTORY_HIDDEN") {
		t.Fatalf("hidden group link err = %v, want MEGAGROUP_PREHISTORY_HIDDEN", err)
	}
}

func TestChannelDiscussionRepliesRPCUsesLinkedMegagroup(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 91, Phone: "15550002911", FirstName: "Owner"})
	member, _ := userStore.Create(ctx, domain.User{AccessHash: 92, Phone: "15550002912", FirstName: "Member"})
	subscriber, _ := userStore.Create(ctx, domain.User{AccessHash: 93, Phone: "15550002913", FirstName: "Subscriber"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	trackedChannels := &countingDiscussionReadChannels{ChannelsService: channelService, delegate: channelService}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: trackedChannels,
	}, zaptest.NewLogger(t), clock.System)
	broadcast, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Discussion Source",
		Broadcast: true,
		Date:      1700002911,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	group, err := channelService.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "Discussion Replies",
		MemberUserIDs: []int64{member.ID},
		Date:          1700002912,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	inputBroadcast := &tg.InputChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash}
	inputGroup := &tg.InputChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash}
	if ok, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, owner.ID), &tg.ChannelsSetDiscussionGroupRequest{
		Broadcast: inputBroadcast,
		Group:     inputGroup,
	}); err != nil || !ok {
		t.Fatalf("set discussion group = ok %v err %v, want true", ok, err)
	}
	if _, err := channelService.InviteToChannel(ctx, owner.ID, broadcast.Channel.ID, []int64{subscriber.ID}, 1700002913); err != nil {
		t.Fatalf("invite subscriber to broadcast: %v", err)
	}
	guestView, err := channelService.GetChannel(ctx, subscriber.ID, group.Channel.ID)
	if err != nil {
		t.Fatalf("linked private-group lookup for subscriber: %v", err)
	}
	if !guestView.Self.Guest || guestView.Self.Status != domain.ChannelMemberLeft {
		t.Fatalf("linked private-group self = %+v, want computed left guest", guestView.Self)
	}
	linkedView, err := channelService.GetLinkedDiscussionChannel(ctx, subscriber.ID, broadcast.Channel.ID)
	if err != nil {
		t.Fatalf("linked discussion projection for subscriber: %v", err)
	}
	if linkedView.Channel.ID != group.Channel.ID || linkedView.Self.Status != domain.ChannelMemberLeft {
		t.Fatalf("linked discussion view = %+v, want group %d with left membership", linkedView, group.Channel.ID)
	}
	fullForSubscriber, err := r.onChannelsGetFullChannel(WithUserID(ctx, subscriber.ID), inputBroadcast)
	if err != nil {
		t.Fatalf("get full broadcast for subscriber: %v", err)
	}
	var fullLinked *tg.Channel
	for _, chat := range fullForSubscriber.Chats {
		if channel, ok := chat.(*tg.Channel); ok && channel.ID == group.Channel.ID {
			fullLinked = channel
		}
	}
	if fullLinked == nil || !fullLinked.Left {
		t.Fatalf("subscriber full chats = %+v, want linked group %d projected as left", fullForSubscriber.Chats, group.Channel.ID)
	}

	postUpdates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		Message:  "channel post",
		RandomID: 2911001,
	})
	if err != nil {
		t.Fatalf("send broadcast post: %v", err)
	}
	post := postUpdates.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if !post.Post {
		t.Fatalf("broadcast post = %#v, want channel post", post)
	}
	historyDomain, err := channelService.GetHistory(ctx, subscriber.ID, domain.ChannelHistoryFilter{
		ChannelID: broadcast.Channel.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("get broadcast history for subscriber: %v", err)
	}
	historyForSubscriber := r.tgChannelHistoryMessages(ctx, subscriber.ID, r.enrichChannelHistory(ctx, subscriber.ID, historyDomain))
	_, historyChats, _ := searchMessagesPayload(t, historyForSubscriber)
	var historyLinked *tg.Channel
	for _, chat := range historyChats {
		if channel, ok := chat.(*tg.Channel); ok && channel.ID == group.Channel.ID {
			historyLinked = channel
		}
	}
	if historyLinked == nil || !historyLinked.Left {
		t.Fatalf("subscriber history chats = %+v, want linked group %d projected as left", historyChats, group.Channel.ID)
	}
	historyMessages, _, _ := searchMessagesPayload(t, historyForSubscriber)
	historyPost := historyMessages[0].(*tg.Message)
	if replies, ok := historyPost.GetReplies(); !ok || !replies.Comments {
		t.Fatalf("subscriber history post replies = %+v ok %v, want comments", replies, ok)
	}
	discussionForSubscriber, err := r.onMessagesGetDiscussionMessage(WithUserID(ctx, subscriber.ID), &tg.MessagesGetDiscussionMessageRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		MsgID: post.ID,
	})
	if err != nil {
		t.Fatalf("get discussion message for subscriber: %v", err)
	}
	var discussionLinked *tg.Channel
	for _, chat := range discussionForSubscriber.Chats {
		if channel, ok := chat.(*tg.Channel); ok && channel.ID == group.Channel.ID {
			discussionLinked = channel
		}
	}
	if discussionLinked == nil || !discussionLinked.Left {
		t.Fatalf("subscriber discussion chats = %+v, want linked group %d projected as left", discussionForSubscriber.Chats, group.Channel.ID)
	}
	if _, err := channelService.InviteToChannel(ctx, owner.ID, group.Channel.ID, []int64{subscriber.ID}, 1700002914); err != nil {
		t.Fatalf("invite subscriber to linked group: %v", err)
	}
	fullAfterJoin, err := r.onChannelsGetFullChannel(WithUserID(ctx, subscriber.ID), inputBroadcast)
	if err != nil {
		t.Fatalf("get cached full broadcast after linked-group join: %v", err)
	}
	var joinedLinked *tg.Channel
	for _, chat := range fullAfterJoin.Chats {
		if channel, ok := chat.(*tg.Channel); ok && channel.ID == group.Channel.ID {
			joinedLinked = channel
		}
	}
	if joinedLinked == nil || joinedLinked.Left {
		t.Fatalf("cached full chats after join = %+v, want refreshed active linked group %d", fullAfterJoin.Chats, group.Channel.ID)
	}
	discussion, err := r.onMessagesGetDiscussionMessage(WithUserID(ctx, owner.ID), &tg.MessagesGetDiscussionMessageRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		MsgID: post.ID,
	})
	if err != nil {
		t.Fatalf("get discussion message: %v", err)
	}
	if len(discussion.Messages) != 1 || len(discussion.Chats) != 2 {
		t.Fatalf("discussion = %+v, want linked root message with source and group chats", discussion)
	}
	root, ok := discussion.Messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("discussion root = %T, want message", discussion.Messages[0])
	}
	if peer, ok := root.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != group.Channel.ID {
		t.Fatalf("discussion root peer = %#v, want linked group %d", root.PeerID, group.Channel.ID)
	}
	if from, ok := root.FromID.(*tg.PeerChannel); !ok || from.ChannelID != broadcast.Channel.ID {
		t.Fatalf("discussion root from = %#v, want source channel %d", root.FromID, broadcast.Channel.ID)
	}
	fwd, ok := root.GetFwdFrom()
	if !ok {
		t.Fatalf("discussion root fwd_from missing")
	}
	if channelPost, ok := fwd.GetChannelPost(); !ok || channelPost != post.ID {
		t.Fatalf("discussion root channel_post = %d ok %v, want %d", channelPost, ok, post.ID)
	}
	if savedMsgID, ok := fwd.GetSavedFromMsgID(); !ok || savedMsgID != post.ID {
		t.Fatalf("discussion root saved_from_msg_id = %d ok %v, want %d", savedMsgID, ok, post.ID)
	}
	if savedPeer, ok := fwd.GetSavedFromPeer(); !ok {
		t.Fatalf("discussion root saved_from_peer missing")
	} else if savedChannel, ok := savedPeer.(*tg.PeerChannel); !ok || savedChannel.ChannelID != broadcast.Channel.ID {
		t.Fatalf("discussion root saved_from_peer = %#v, want source channel %d", savedPeer, broadcast.Channel.ID)
	}

	replyTo := &tg.InputReplyToMessage{ReplyToMsgID: root.ID}
	replyUpdates, err := r.onMessagesSendMessage(WithUserID(ctx, member.ID), func() *tg.MessagesSendMessageRequest {
		req := &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash},
			Message:  "discussion reply",
			RandomID: 2911002,
		}
		req.SetReplyTo(replyTo)
		return req
	}())
	if err != nil {
		t.Fatalf("send discussion reply: %v", err)
	}
	comment := replyUpdates.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	replies, err := r.onMessagesGetReplies(WithUserID(ctx, owner.ID), &tg.MessagesGetRepliesRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		MsgID: post.ID,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get replies: %v", err)
	}
	replyMessages, replyChats, _ := searchMessagesPayload(t, replies)
	if len(replyMessages) != 1 || len(replyChats) != 2 {
		t.Fatalf("get replies = %T %+v, want one linked group reply with both channel contexts", replies, replies)
	}
	gotReply := replyMessages[0].(*tg.Message)
	if gotReply.ID != comment.ID || gotReply.Message != "discussion reply" {
		t.Fatalf("reply message = %#v, want comment id %d", gotReply, comment.ID)
	}
	if peer, ok := gotReply.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != group.Channel.ID {
		t.Fatalf("reply peer = %#v, want linked group %d", gotReply.PeerID, group.Channel.ID)
	}
	header, ok := gotReply.ReplyTo.(*tg.MessageReplyHeader)
	if !ok {
		t.Fatalf("reply header = %#v, want messageReplyHeader", gotReply.ReplyTo)
	}
	topID, topOK := header.GetReplyToTopID()
	if header.ReplyToMsgID != root.ID || !topOK || topID != root.ID {
		t.Fatalf("reply header = %#v, want msg/top %d", header, root.ID)
	}
	views, err := r.onMessagesGetMessagesViews(WithUserID(ctx, owner.ID), &tg.MessagesGetMessagesViewsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		ID:   []int{post.ID},
	})
	if err != nil {
		t.Fatalf("get message views: %v", err)
	}
	replyInfo, ok := views.Views[0].GetReplies()
	if !ok || !replyInfo.Comments || replyInfo.Replies != 1 {
		t.Fatalf("message views replies = %+v ok %v, want one comment", replyInfo, ok)
	}
	if channelID, ok := replyInfo.GetChannelID(); !ok || channelID != group.Channel.ID {
		t.Fatalf("message views channel_id = %d ok %v, want %d", channelID, ok, group.Channel.ID)
	}
	if maxID, ok := replyInfo.GetMaxID(); !ok || maxID != comment.ID {
		t.Fatalf("message views max_id = %d ok %v, want %d", maxID, ok, comment.ID)
	}
	readTarget, err := channelService.ResolveDiscussionReadTarget(ctx, owner.ID, broadcast.Channel.ID, post.ID, comment.ID)
	if err != nil {
		t.Fatalf("resolve discussion read target before read: %v", err)
	}
	if readTarget.ChannelID != group.Channel.ID || readTarget.RootID != root.ID || readTarget.AlreadyRead {
		t.Fatalf("read target before read = %+v, want group/root and unread", readTarget)
	}
	trackedChannels.resolveCalls = 0
	trackedChannels.getDiscussionCalls = 0
	if ok, err := r.onMessagesReadDiscussion(WithUserID(ctx, owner.ID), &tg.MessagesReadDiscussionRequest{
		Peer:      &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		MsgID:     post.ID,
		ReadMaxID: comment.ID,
	}); err != nil || !ok {
		t.Fatalf("read discussion = ok %v err %v, want true", ok, err)
	}
	if trackedChannels.resolveCalls != 1 || trackedChannels.getDiscussionCalls != 0 {
		t.Fatalf("first read calls resolve=%d getDiscussion=%d, want narrow resolver only", trackedChannels.resolveCalls, trackedChannels.getDiscussionCalls)
	}
	readTarget, err = channelService.ResolveDiscussionReadTarget(ctx, owner.ID, broadcast.Channel.ID, post.ID, comment.ID)
	if err != nil || !readTarget.AlreadyRead {
		t.Fatalf("resolve discussion read target after read = %+v err %v, want already read", readTarget, err)
	}
	if changed, err := r.onMessagesReadDiscussion(WithUserID(ctx, owner.ID), &tg.MessagesReadDiscussionRequest{
		Peer:      &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		MsgID:     post.ID,
		ReadMaxID: comment.ID,
	}); err != nil || changed {
		t.Fatalf("repeat read discussion = changed %v err %v, want idempotent false", changed, err)
	}
	if trackedChannels.resolveCalls != 2 || trackedChannels.getDiscussionCalls != 0 {
		t.Fatalf("repeat read calls resolve=%d getDiscussion=%d, want narrow resolver only", trackedChannels.resolveCalls, trackedChannels.getDiscussionCalls)
	}
	afterRead, err := r.onMessagesGetDiscussionMessage(WithUserID(ctx, owner.ID), &tg.MessagesGetDiscussionMessageRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		MsgID: post.ID,
	})
	if err != nil {
		t.Fatalf("get discussion after read: %v", err)
	}
	if afterRead.ReadInboxMaxID != comment.ID || afterRead.UnreadCount != 0 {
		t.Fatalf("discussion after read = %+v, want read inbox %d and no unread", afterRead, comment.ID)
	}
}
