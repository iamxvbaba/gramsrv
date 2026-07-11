package rpc

import (
	"context"
	"errors"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"
	"strings"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestMessagesSearchChannelPeerReturnsSingleCopyMessages(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 35, Phone: "15550002035", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 36, Phone: "15550002036", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "RPC Search Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	pinnedMsgID := 0
	for _, item := range []struct {
		userID int64
		text   string
		random int64
	}{
		{owner.ID, "needle from owner", 5001},
		{friend.ID, "not this one", 5002},
		{friend.ID, "needle from friend", 5003},
	} {
		sent, err := r.onMessagesSendMessage(WithUserID(ctx, item.userID), &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
			Message:  item.text,
			RandomID: item.random,
		})
		if err != nil {
			t.Fatalf("send %q: %v", item.text, err)
		}
		if item.text == "not this one" {
			channelUpdates := sent.(*tg.Updates)
			if len(channelUpdates.Updates) == 0 {
				t.Fatalf("send %q updates = %+v, want updateMessageID", item.text, channelUpdates.Updates)
			}
			pinnedMsgID = channelUpdates.Updates[0].(*tg.UpdateMessageID).ID
		}
	}

	req := &tg.MessagesSearchRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Q:      "needle",
		Filter: &tg.InputMessagesFilterEmpty{},
		Limit:  10,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode search: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, friend.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch search: %v", err)
	}
	messages, chats, users := searchMessagesPayload(t, enc)
	if len(messages) != 2 || len(chats) != 1 || len(users) < 2 {
		t.Fatalf("search payload sizes = messages %d chats %d users %d, want 2/1/2+", len(messages), len(chats), len(users))
	}
	for _, msg := range messages {
		item := msg.(*tg.Message)
		if !strings.Contains(item.Message, "needle") {
			t.Fatalf("search result message = %q, want only needle hits", item.Message)
		}
	}

	fromReq := *req
	fromReq.SetFromID(&tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash})
	in.Reset()
	if err := fromReq.Encode(&in); err != nil {
		t.Fatalf("encode from search: %v", err)
	}
	enc, err = r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch from search: %v", err)
	}
	messages, _, _ = searchMessagesPayload(t, enc)
	if len(messages) != 1 || messages[0].(*tg.Message).Message != "needle from friend" {
		t.Fatalf("from search messages = %+v, want friend needle only", messages)
	}

	mediaCountReq := &tg.MessagesSearchRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter: &tg.InputMessagesFilterPhotos{},
		Limit:  0,
	}
	in.Reset()
	if err := mediaCountReq.Encode(&in); err != nil {
		t.Fatalf("encode shared media count search: %v", err)
	}
	enc, err = r.Dispatch(WithUserID(ctx, friend.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch shared media count search: %v", err)
	}
	if box, ok := enc.(*tg.MessagesMessagesBox); ok {
		enc = box.Messages
	}
	channelMessages, ok := enc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("shared media count search result = %T, want messages.channelMessages", enc)
	}
	if channelMessages.Count != 0 || len(channelMessages.Messages) != 0 {
		t.Fatalf("shared media count search = count %d messages %d, want empty without media store", channelMessages.Count, len(channelMessages.Messages))
	}

	if _, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, owner.ID), &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   pinnedMsgID,
	}); err != nil {
		t.Fatalf("pin channel message: %v", err)
	}
	pinnedReq := &tg.MessagesSearchRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter: &tg.InputMessagesFilterPinned{},
		Limit:  40,
	}
	in.Reset()
	if err := pinnedReq.Encode(&in); err != nil {
		t.Fatalf("encode pinned search: %v", err)
	}
	enc, err = r.Dispatch(WithUserID(ctx, friend.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch pinned search: %v", err)
	}
	pinnedMessages, _, _ := searchMessagesPayload(t, enc)
	if len(pinnedMessages) != 1 {
		t.Fatalf("pinned search returned %d messages, want 1", len(pinnedMessages))
	}
	pinnedMessage, ok := pinnedMessages[0].(*tg.Message)
	if !ok || pinnedMessage.ID != pinnedMsgID || !pinnedMessage.GetPinned() {
		t.Fatalf("pinned search message = %#v, want pinned message id=%d", pinnedMessages[0], pinnedMsgID)
	}
}

func TestMessagesSearchChatPhotosDoesNotReturnOrdinaryChannelHistory(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 93500, Phone: "15550093500", FirstName: "Owner"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Channels: channelService,
	}, zaptest.NewLogger(t), clock.System)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title: "Chat Photos Count Guard", Megagroup: true, Date: 1700034000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.Channel
	for i := 0; i < 7; i++ {
		if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
			ChannelID: channel.ID,
			RandomID:  int64(1700034000 + i),
			Message:   "ordinary channel message",
			Date:      1700034001 + i,
		}); err != nil {
			t.Fatalf("send ordinary message %d: %v", i, err)
		}
	}

	req := &tg.MessagesSearchRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter: &tg.InputMessagesFilterChatPhotos{},
		Limit:  80,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode chat photos search: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch chat photos search: %v", err)
	}
	if box, ok := enc.(*tg.MessagesMessagesBox); ok {
		enc = box.Messages
	}
	got, ok := enc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("chat photos search result = %T, want messages.channelMessages", enc)
	}
	if got.Count != 0 || len(got.Messages) != 0 {
		t.Fatalf("chat photos search = count %d messages %d, want explicit empty stub", got.Count, len(got.Messages))
	}
	if len(got.Chats) != 1 {
		t.Fatalf("chat photos search chats = %d, want current channel projection", len(got.Chats))
	}
}

func TestMessagesGetSearchCountersUsesMediaCategoryCounts(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 93501, Phone: "15550093501", FirstName: "Owner"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Channels: channelService,
	}, zaptest.NewLogger(t), clock.System)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title: "Media Counters", Megagroup: true, Date: 1700035000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.Channel
	docMedia := func(id int64, attrs ...domain.DocumentAttribute) *domain.MessageMedia {
		return &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &domain.Document{ID: id, AccessHash: id + 10, Attributes: attrs}}
	}
	send := func(randomID int64, msg string, media *domain.MessageMedia, entities []domain.MessageEntity) {
		if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
			ChannelID: channel.ID,
			RandomID:  randomID,
			Message:   msg,
			Media:     media,
			Entities:  entities,
			Date:      1700035000 + int(randomID),
		}); err != nil {
			t.Fatalf("send %d: %v", randomID, err)
		}
	}
	send(1, "photo https://x.test", &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 1, AccessHash: 11}}, []domain.MessageEntity{{Type: domain.MessageEntityURL, Offset: 6, Length: 14}})
	send(2, "video", docMedia(2, domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: 1, H: 1}), nil)
	send(3, "file", docMedia(3, domain.DocumentAttribute{Kind: domain.DocAttrFilename, FileName: "a.bin"}), nil)
	send(4, "music", docMedia(4, domain.DocumentAttribute{Kind: domain.DocAttrAudio, Title: "song"}), nil)
	send(5, "voice", docMedia(5, domain.DocumentAttribute{Kind: domain.DocAttrAudio, Voice: true}), nil)
	send(6, "round", docMedia(6, domain.DocumentAttribute{Kind: domain.DocAttrVideo, RoundMessage: true}), nil)
	gif := docMedia(7,
		domain.DocumentAttribute{Kind: domain.DocAttrAnimated},
		domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: 320, H: 240, Duration: 1})
	gif.Document.MimeType = "video/mp4"
	send(7, "gif", gif, nil)
	send(8, "poll", &domain.MessageMedia{Kind: domain.MessageMediaKindPoll}, nil)

	filters := []tg.MessagesFilterClass{
		&tg.InputMessagesFilterPhotos{},
		&tg.InputMessagesFilterPhotoVideo{},
		&tg.InputMessagesFilterDocument{},
		&tg.InputMessagesFilterMusic{},
		&tg.InputMessagesFilterURL{},
		&tg.InputMessagesFilterGif{},
		&tg.InputMessagesFilterVoice{},
		&tg.InputMessagesFilterRoundVideo{},
		&tg.InputMessagesFilterRoundVoice{},
		&tg.InputMessagesFilterPoll{},
		&tg.InputMessagesFilterChatPhotos{},
	}
	counters, err := r.onMessagesGetSearchCounters(WithUserID(ctx, owner.ID), &tg.MessagesGetSearchCountersRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filters: filters,
	})
	if err != nil {
		t.Fatalf("messages.getSearchCounters: %v", err)
	}
	want := []int{1, 2, 1, 1, 1, 1, 1, 1, 2, 1, 0}
	if len(counters) != len(want) {
		t.Fatalf("got %d counters, want %d", len(counters), len(want))
	}
	for i, counter := range counters {
		if counter.Count != want[i] {
			t.Fatalf("counter[%d] %T = %d, want %d", i, counter.Filter, counter.Count, want[i])
		}
		if counter.Inexact {
			t.Fatalf("counter[%d] is inexact, want exact", i)
		}
	}

	searchReq := &tg.MessagesSearchRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter: &tg.InputMessagesFilterPhotoVideo{},
		Limit:  0,
	}
	var in bin.Buffer
	if err := searchReq.Encode(&in); err != nil {
		t.Fatalf("encode shared media count search: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch shared media count search: %v", err)
	}
	if box, ok := enc.(*tg.MessagesMessagesBox); ok {
		enc = box.Messages
	}
	channelMessages, ok := enc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("shared media count search result = %T, want messages.channelMessages", enc)
	}
	if channelMessages.Count != 2 || len(channelMessages.Messages) != 0 {
		t.Fatalf("shared media count search = count %d messages %d, want count 2 and no messages", channelMessages.Count, len(channelMessages.Messages))
	}
}

func TestMessagesGetSearchCountersPropagatesMediaCountError(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 93502, Phone: "15550093502", FirstName: "Owner"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title: "Media Counter Error", Megagroup: true, Date: 1700035100,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	wantErr := errors.New("media count store down")
	r := New(Config{}, Deps{
		Channels: &failingMediaCountChannelsService{Service: channelService, err: wantErr},
	}, zaptest.NewLogger(t), clock.System)

	_, err = r.onMessagesGetSearchCounters(WithUserID(ctx, owner.ID), &tg.MessagesGetSearchCountersRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
		Filters: []tg.MessagesFilterClass{&tg.InputMessagesFilterURL{}},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("messages.getSearchCounters error = %v, want %v", err, wantErr)
	}
}

type failingMediaCountChannelsService struct {
	*appchannels.Service
	err error
}

func (s *failingMediaCountChannelsService) CountChannelMediaCategories(context.Context, int64, int64) (domain.MediaCategoryCounts, error) {
	return nil, s.err
}

func TestMessagesSearchGlobalReturnsJoinedChannelMessages(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 91101, Phone: "15550091101", FirstName: "Owner"})
	viewer, _ := userStore.Create(ctx, domain.User{AccessHash: 91102, Phone: "15550091102", FirstName: "Viewer"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), clock.System)
	joined, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Joined Broadcast",
		Broadcast: true,
		Date:      1700020000,
	})
	if err != nil {
		t.Fatalf("create joined channel: %v", err)
	}
	if _, err := channelService.InviteToChannel(ctx, owner.ID, joined.Channel.ID, []int64{viewer.ID}, 1700020001); err != nil {
		t.Fatalf("invite viewer: %v", err)
	}
	hidden, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Hidden Broadcast",
		Broadcast: true,
		Date:      1700020002,
	})
	if err != nil {
		t.Fatalf("create hidden channel: %v", err)
	}
	for i, body := range []string{"global needle older", "global needle newer"} {
		if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
			ChannelID: joined.Channel.ID,
			RandomID:  int64(201 + i),
			Message:   body,
			Date:      1700020010 + i*10,
		}); err != nil {
			t.Fatalf("send joined %d: %v", i, err)
		}
	}
	if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: hidden.Channel.ID,
		RandomID:  301,
		Message:   "global needle hidden",
		Date:      1700020030,
	}); err != nil {
		t.Fatalf("send hidden: %v", err)
	}

	req := &tg.MessagesSearchGlobalRequest{
		BroadcastsOnly: true,
		Q:              "global needle",
		Filter:         &tg.InputMessagesFilterEmpty{},
		OffsetPeer:     &tg.InputPeerEmpty{},
		Limit:          1,
	}
	got, err := r.onMessagesSearchGlobal(WithUserID(ctx, viewer.ID), req)
	if err != nil {
		t.Fatalf("messages.searchGlobal first page: %v", err)
	}
	slice, ok := got.(*tg.MessagesMessagesSlice)
	if !ok {
		t.Fatalf("first page = %T %+v, want messagesSlice", got, got)
	}
	if nextRate, ok := slice.GetNextRate(); !ok || nextRate != 1700020020 {
		t.Fatalf("first page next_rate = %d ok %v, want newest date", nextRate, ok)
	}
	messages, chats, users := searchMessagesPayload(t, got)
	if len(messages) != 1 || len(chats) != 1 || len(users) != 1 {
		t.Fatalf("first payload messages=%d chats=%d users=%d, want 1/1/1", len(messages), len(chats), len(users))
	}
	first := messages[0].(*tg.Message)
	if first.Message != "global needle newer" {
		t.Fatalf("first message = %q, want newest joined channel hit", first.Message)
	}
	if peer, ok := first.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != joined.Channel.ID {
		t.Fatalf("first peer = %#v, want joined channel %d", first.PeerID, joined.Channel.ID)
	}

	page2 := &tg.MessagesSearchGlobalRequest{
		BroadcastsOnly: true,
		Q:              "global needle",
		Filter:         &tg.InputMessagesFilterEmpty{},
		OffsetRate:     slice.NextRate,
		OffsetPeer: &tg.InputPeerChannel{
			ChannelID:  joined.Channel.ID,
			AccessHash: joined.Channel.AccessHash,
		},
		OffsetID: first.ID,
		Limit:    10,
	}
	got, err = r.onMessagesSearchGlobal(WithUserID(ctx, viewer.ID), page2)
	if err != nil {
		t.Fatalf("messages.searchGlobal second page: %v", err)
	}
	messages, chats, _ = searchMessagesPayload(t, got)
	if len(messages) != 1 || len(chats) != 1 {
		t.Fatalf("second payload messages=%d chats=%d, want older joined hit only", len(messages), len(chats))
	}
	if msg := messages[0].(*tg.Message); msg.Message != "global needle older" {
		t.Fatalf("second message = %q, want older joined hit", msg.Message)
	}
}

func TestMessagesGetHistoryReturnsStoredMessages(t *testing.T) {
	msg := domain.Message{
		ID:          1,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000100,
		Body:        "Login code: 12345",
	}
	messages := &captureMessages{
		list: domain.MessageList{
			Messages: []domain.Message{msg},
			Users:    []domain.User{domain.OfficialSystemUser()},
			Count:    1,
			Hash:     99,
		},
	}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetHistoryRequest{
		Peer:      &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash},
		Limit:     20,
		AddOffset: 1 << 30,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.MessagesMessagesBox)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesMessagesBox", enc)
	}
	got, ok := box.Messages.(*tg.MessagesMessages)
	if !ok {
		t.Fatalf("boxed response = %T, want *tg.MessagesMessages", box.Messages)
	}
	if len(got.Messages) != 1 || len(got.Users) != 1 {
		t.Fatalf("history = %+v, want one message and one user", got)
	}
	if messages.filter.Peer.ID != domain.OfficialSystemUserID || messages.filter.Limit != 20 || messages.filter.AddOffset != domain.MaxMessageHistoryAddOffset {
		t.Fatalf("filter = %+v, want official peer limit 20 and clamped add_offset", messages.filter)
	}
}

func TestMessagesSetTypingPushesUserTypingUpdate(t *testing.T) {
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	var authKeyID [8]byte
	authKeyID[0] = 7

	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 55)
	ok, err := r.onMessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{
		Peer:   &tg.InputPeerUser{UserID: 1000000002, AccessHash: 22},
		Action: &tg.SendMessageTypingAction{},
	})
	if err != nil {
		t.Fatalf("set typing: %v", err)
	}
	if !ok {
		t.Fatalf("set typing = false, want true")
	}

	got := sessions.snapshot()
	if got.userID != 1000000002 || got.sessionID != 55 || got.messageType != proto.MessageFromServer {
		t.Fatalf("push = user %d exclude session %d type %v, want target/exclude/from_server", got.userID, got.sessionID, got.messageType)
	}
	if gotAuthKeyID := sessions.scopedAuthKey(); gotAuthKeyID != authKeyID {
		t.Fatalf("exclude auth_key_id = %x, want %x", gotAuthKeyID, authKeyID)
	}
	updateShort, ok := got.message.(*tg.UpdateShort)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.UpdateShort", got.message)
	}
	typing, ok := updateShort.Update.(*tg.UpdateUserTyping)
	if !ok {
		t.Fatalf("short update = %T, want *tg.UpdateUserTyping", updateShort.Update)
	}
	if typing.UserID != 1000000001 {
		t.Fatalf("typing user_id = %d, want sender", typing.UserID)
	}
	if _, ok := typing.Action.(*tg.SendMessageTypingAction); !ok {
		t.Fatalf("typing action = %T, want *tg.SendMessageTypingAction", typing.Action)
	}
}

func TestMessagesSetTypingRejectsInvalidTopMsgID(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesSetTypingRequest{
		Peer:   &tg.InputPeerUser{UserID: 1000000002, AccessHash: 22},
		Action: &tg.SendMessageTypingAction{},
	}
	req.SetTopMsgID(domain.MaxMessageBoxID + 1)

	ok, err := r.onMessagesSetTyping(WithUserID(context.Background(), 1000000001), req)
	if ok || err == nil || !strings.Contains(err.Error(), "MSG_ID_INVALID") {
		t.Fatalf("set typing invalid top_msg_id = ok %v err %v, want MSG_ID_INVALID", ok, err)
	}
}

func TestMessagesSetTypingTreatsWebAMainThreadSentinelAsUnset(t *testing.T) {
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesSetTypingRequest{
		Peer:   &tg.InputPeerUser{UserID: 1000000002, AccessHash: 22},
		Action: &tg.SendMessageTypingAction{},
	}
	req.SetTopMsgID(-1)

	ok, err := r.onMessagesSetTyping(WithUserID(context.Background(), 1000000001), req)
	if err != nil {
		t.Fatalf("set typing with WebA main thread sentinel: %v", err)
	}
	if !ok {
		t.Fatalf("set typing = false, want true")
	}
	got := sessions.snapshot()
	updateShort, ok := got.message.(*tg.UpdateShort)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.UpdateShort", got.message)
	}
	typing, ok := updateShort.Update.(*tg.UpdateUserTyping)
	if !ok {
		t.Fatalf("short update = %T, want *tg.UpdateUserTyping", updateShort.Update)
	}
	if typing.TopMsgID != 0 {
		t.Fatalf("typing top_msg_id = %d, want unset/0", typing.TopMsgID)
	}
}

func TestMessagesSetTypingPushesChannelTypingTopMsgID(t *testing.T) {
	const (
		ownerID  = int64(1000000001)
		memberID = int64(1000000002)
		topicID  = 7
	)
	channels := appchannels.NewService(memory.NewChannelStore())
	created, err := channels.CreateChannel(context.Background(), ownerID, domain.CreateChannelRequest{
		Title:         "topic group",
		CreatorUserID: ownerID,
		Megagroup:     true,
		MemberUserIDs: []int64{memberID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sessions := &captureScopedSessions{captureSessions: &captureSessions{
		channelViewers: map[int64][]int64{created.Channel.ID: {memberID}},
	}}
	r := New(Config{}, Deps{Channels: channels, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	var authKeyID [8]byte
	authKeyID[0] = 9

	req := &tg.MessagesSetTypingRequest{
		Peer: &tg.InputPeerChannel{
			ChannelID:  created.Channel.ID,
			AccessHash: created.Channel.AccessHash,
		},
		Action: &tg.SendMessageTypingAction{},
	}
	req.SetTopMsgID(topicID)
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), ownerID), authKeyID), 77)

	ok, err := r.onMessagesSetTyping(ctx, req)
	if err != nil {
		t.Fatalf("set channel typing: %v", err)
	}
	if !ok {
		t.Fatalf("set channel typing = false, want true")
	}
	got := sessions.snapshot()
	if got.userID != memberID || got.sessionID != 77 || got.messageType != proto.MessageFromServer {
		t.Fatalf("channel typing push = user %d exclude session %d type %v, want member/exclude/from_server", got.userID, got.sessionID, got.messageType)
	}
	if gotAuthKeyID := sessions.scopedAuthKey(); gotAuthKeyID != authKeyID {
		t.Fatalf("exclude auth_key_id = %x, want %x", gotAuthKeyID, authKeyID)
	}
	updates, ok := got.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", got.message)
	}
	if len(updates.Updates) != 1 {
		t.Fatalf("updates len = %d, want 1", len(updates.Updates))
	}
	typing, ok := updates.Updates[0].(*tg.UpdateChannelUserTyping)
	if !ok {
		t.Fatalf("channel update = %T, want *tg.UpdateChannelUserTyping", updates.Updates[0])
	}
	if typing.ChannelID != created.Channel.ID || typing.TopMsgID != topicID {
		t.Fatalf("channel typing = channel %d top %d, want channel %d top %d", typing.ChannelID, typing.TopMsgID, created.Channel.ID, topicID)
	}
	from, ok := typing.FromID.(*tg.PeerUser)
	if !ok || from.UserID != ownerID {
		t.Fatalf("typing from = %T %+v, want owner peer", typing.FromID, typing.FromID)
	}
	if _, ok := typing.Action.(*tg.SendMessageTypingAction); !ok {
		t.Fatalf("typing action = %T, want *tg.SendMessageTypingAction", typing.Action)
	}
}

func TestMessagesSetTypingSkipsChannelMemberWithoutViewerInterest(t *testing.T) {
	const (
		ownerID  = int64(1000000001)
		memberID = int64(1000000002)
	)
	channels := appchannels.NewService(memory.NewChannelStore())
	created, err := channels.CreateChannel(context.Background(), ownerID, domain.CreateChannelRequest{
		Title:         "quiet group",
		CreatorUserID: ownerID,
		Megagroup:     true,
		MemberUserIDs: []int64{memberID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sessions := &captureScopedSessions{captureSessions: &captureSessions{
		channelMembers: map[int64][]int64{created.Channel.ID: {memberID}},
	}}
	r := New(Config{}, Deps{Channels: channels, Sessions: sessions}, zaptest.NewLogger(t), clock.System)

	ok, err := r.onMessagesSetTyping(WithUserID(context.Background(), ownerID), &tg.MessagesSetTypingRequest{
		Peer: &tg.InputPeerChannel{
			ChannelID:  created.Channel.ID,
			AccessHash: created.Channel.AccessHash,
		},
		Action: &tg.SendMessageTypingAction{},
	})
	if err != nil {
		t.Fatalf("set channel typing: %v", err)
	}
	if !ok {
		t.Fatalf("set channel typing = false, want true")
	}
	if got := sessions.snapshot(); got.message != nil {
		t.Fatalf("typing push without viewer interest = %+v, want none", got)
	}
}

func TestMessagesGetMessagesReturnsOwnerMessages(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	messages := &captureMessages{list: domain.MessageList{
		Messages: []domain.Message{{
			ID:          7,
			OwnerUserID: userID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
			Date:        1700000000,
			Body:        "reply source",
		}},
		Count: 1,
	}}
	r := New(Config{}, Deps{
		Messages: messages,
		Users: mapUsersService{users: map[int64]domain.User{
			userID: {ID: userID, FirstName: "Alice"},
			peerID: {ID: peerID, FirstName: "Bob"},
		}},
	}, zaptest.NewLogger(t), clock.System)

	got, err := r.onMessagesGetMessages(WithUserID(context.Background(), userID), []tg.InputMessageClass{&tg.InputMessageID{ID: 7}})
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	box, ok := got.(*tg.MessagesMessages)
	if !ok || len(box.Messages) != 1 {
		t.Fatalf("response = %T %+v, want one messages.messages", got, got)
	}
	msg, ok := box.Messages[0].(*tg.Message)
	if !ok || msg.ID != 7 || msg.Message != "reply source" {
		t.Fatalf("message = %#v, want source message", box.Messages[0])
	}
	if len(box.Users) != 1 {
		t.Fatalf("users = %+v, want peer user", box.Users)
	}
}
