package rpc

import (
	"context"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strings"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestMessagesForwardMessagesRecordsRequestAndReturnsUpdates(t *testing.T) {
	const (
		ownerID = int64(1000000001)
		fromID  = int64(1000000002)
		toID    = int64(1000000003)
	)
	// 私聊→私聊转发统一经 forwardSources 取源（GetMessages）再逐条 SendPrivateText，
	// 以便在 RPC 层对原作者做 PrivacyKeyForwards 降级，故必须提供源消息。
	messages := &captureMessages{list: domain.MessageList{Messages: []domain.Message{
		{ID: 3, OwnerUserID: ownerID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: fromID}, From: domain.Peer{Type: domain.PeerTypeUser, ID: fromID}, Date: 1700000000, Body: "first"},
		{ID: 4, OwnerUserID: ownerID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: fromID}, From: domain.Peer{Type: domain.PeerTypeUser, ID: fromID}, Date: 1700000001, Body: "second"},
	}}}
	r := New(Config{}, Deps{
		Messages: messages,
		Users: mapUsersService{users: map[int64]domain.User{
			ownerID: {ID: ownerID, FirstName: "Owner"},
			fromID:  {ID: fromID, FirstName: "From"},
			toID:    {ID: toID, FirstName: "To"},
		}},
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesForwardMessagesRequest{
		FromPeer:   &tg.InputPeerUser{UserID: fromID},
		ToPeer:     &tg.InputPeerUser{UserID: toID},
		ID:         []int{3, 4},
		RandomID:   []int64{1001, 1002},
		Silent:     true,
		Noforwards: true,
	}
	replyTo := &tg.InputReplyToMessage{ReplyToMsgID: 9}
	replyTo.SetQuoteText("target")
	req.SetReplyTo(replyTo)
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), ownerID), [8]byte{}, 99, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if messages.sendUserID != ownerID || messages.sendReq.SenderUserID != ownerID || messages.sendReq.RecipientUserID != toID || messages.sendReq.OriginSessionID != 99 {
		t.Fatalf("forward send = user %d %+v, want owner/to/session", messages.sendUserID, messages.sendReq)
	}
	if messages.sendReq.ReplyTo == nil || messages.sendReq.ReplyTo.MessageID != 9 || messages.sendReq.ReplyTo.Peer.ID != toID || messages.sendReq.ReplyTo.QuoteText != "target" {
		t.Fatalf("forward reply = %+v, want target peer reply metadata", messages.sendReq.ReplyTo)
	}
	got, ok := enc.(*tg.Updates)
	if !ok {
		t.Fatalf("response = %T, want *tg.Updates", enc)
	}
	if len(got.Updates) != 4 {
		t.Fatalf("updates = %+v, want two message ids and two new messages", got.Updates)
	}
	if id, ok := got.Updates[0].(*tg.UpdateMessageID); !ok || id.RandomID != 1001 {
		t.Fatalf("first update = %#v, want updateMessageID random 1001", got.Updates[0])
	}
	newMsg := got.Updates[1].(*tg.UpdateNewMessage)
	msg := newMsg.Message.(*tg.Message)
	if msg.FwdFrom.Date == 0 || !msg.Silent || !msg.Noforwards {
		t.Fatalf("forwarded message = %#v, want fwd header and flags", msg)
	}
	if header, ok := msg.ReplyTo.(*tg.MessageReplyHeader); !ok || header.ReplyToMsgID != 9 {
		t.Fatalf("forwarded reply = %#v, want reply header id=9", msg.ReplyTo)
	}
	hasForwardAuthor := false
	for _, user := range got.Users {
		if u, ok := user.(*tg.User); ok && u.ID == fromID {
			hasForwardAuthor = true
			break
		}
	}
	if !hasForwardAuthor {
		t.Fatalf("forward users = %+v, want original author %d for fwd_from", got.Users, fromID)
	}
}

func TestMessagesForwardMessagesTreatsMainThreadTopMsgSentinelAsAbsent(t *testing.T) {
	const (
		ownerID = int64(1000000101)
		fromID  = int64(1000000102)
		toID    = int64(1000000103)
	)
	ctx := context.Background()
	messages := &captureMessages{list: domain.MessageList{Messages: []domain.Message{
		{
			ID:          8,
			OwnerUserID: ownerID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: fromID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: fromID},
			Date:        1700000108,
			Body:        "main thread source",
		},
	}}}
	r := New(Config{}, Deps{
		Messages: messages,
		Users: mapUsersService{users: map[int64]domain.User{
			ownerID: {ID: ownerID, FirstName: "Owner"},
			fromID:  {ID: fromID, FirstName: "From"},
			toID:    {ID: toID, FirstName: "To"},
		}},
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerUser{UserID: fromID},
		ToPeer:   &tg.InputPeerUser{UserID: toID},
		ID:       []int{8},
		RandomID: []int64{8001},
	}
	req.SetTopMsgID(-1)

	updatesClass, err := r.onMessagesForwardMessages(WithUserID(ctx, ownerID), req)
	if err != nil {
		t.Fatalf("forward with main thread top_msg_id sentinel: %v", err)
	}
	if messages.sendReq.ReplyTo != nil {
		t.Fatalf("reply = %+v, want nil for main thread sentinel", messages.sendReq.ReplyTo)
	}
	updates, ok := updatesClass.(*tg.Updates)
	if !ok || len(updates.Updates) != 2 {
		t.Fatalf("updates = %T %+v, want updateMessageID + updateNewMessage", updatesClass, updatesClass)
	}
}

func TestMessagesForwardMessagesLoadsPrivateSourcesInSingleBatch(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{
		AccessHash: 41,
		Phone:      "15550004001",
		FirstName:  "ForwardOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sourceUser, err := userStore.Create(ctx, domain.User{
		AccessHash: 42,
		Phone:      "15550004002",
		FirstName:  "ForwardSource",
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	channelSvc := appchannels.NewService(memory.NewChannelStore())
	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Forward Batch",
		Megagroup:     true,
		Date:          1700001200,
	})
	if err != nil {
		t.Fatalf("create target channel: %v", err)
	}
	messages := &captureMessages{
		getMessagesListed: true,
		list: domain.MessageList{Messages: []domain.Message{
			{
				ID:          5,
				OwnerUserID: owner.ID,
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: sourceUser.ID},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: sourceUser.ID},
				Date:        1700001195,
				Body:        "source five",
			},
			{
				ID:          7,
				OwnerUserID: owner.ID,
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: sourceUser.ID},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: sourceUser.ID},
				Date:        1700001197,
				Body:        "source seven",
			},
		}},
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Messages: messages,
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	updatesClass, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerUser{UserID: sourceUser.ID, AccessHash: sourceUser.AccessHash},
		ToPeer: &tg.InputPeerChannel{
			ChannelID:  created.Channel.ID,
			AccessHash: created.Channel.AccessHash,
		},
		ID:       []int{7, 5},
		RandomID: []int64{7001, 5001},
	})
	if err != nil {
		t.Fatalf("forward private sources to channel: %v", err)
	}
	if messages.getMessagesCalls != 1 {
		t.Fatalf("GetMessages calls = %d, want one batched source load", messages.getMessagesCalls)
	}
	if len(messages.getMessagesIDs) != 1 || len(messages.getMessagesIDs[0]) != 2 || messages.getMessagesIDs[0][0] != 7 || messages.getMessagesIDs[0][1] != 5 {
		t.Fatalf("GetMessages ids = %+v, want [[7 5]]", messages.getMessagesIDs)
	}
	updates, ok := updatesClass.(*tg.Updates)
	if !ok || len(updates.Updates) != 4 {
		t.Fatalf("forward updates = %T %+v, want 4 updates", updatesClass, updatesClass)
	}
	firstID, ok := updates.Updates[0].(*tg.UpdateMessageID)
	if !ok || firstID.RandomID != 7001 {
		t.Fatalf("first update = %#v, want random 7001", updates.Updates[0])
	}
	firstNew, ok := updates.Updates[1].(*tg.UpdateNewChannelMessage)
	if !ok {
		t.Fatalf("second update = %#v, want updateNewChannelMessage", updates.Updates[1])
	}
	firstMsg, ok := firstNew.Message.(*tg.Message)
	if !ok || firstMsg.Message != "source seven" {
		t.Fatalf("first forwarded message = %#v, want source seven", firstNew.Message)
	}
	secondID, ok := updates.Updates[2].(*tg.UpdateMessageID)
	if !ok || secondID.RandomID != 5001 {
		t.Fatalf("third update = %#v, want random 5001", updates.Updates[2])
	}
	secondNew, ok := updates.Updates[3].(*tg.UpdateNewChannelMessage)
	if !ok {
		t.Fatalf("fourth update = %#v, want updateNewChannelMessage", updates.Updates[3])
	}
	secondMsg, ok := secondNew.Message.(*tg.Message)
	if !ok || secondMsg.Message != "source five" {
		t.Fatalf("second forwarded message = %#v, want source five", secondNew.Message)
	}
}

func TestMessagesForwardMessagesChannelReplayDoesNotRepeatRealtimePayload(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 51, Phone: "15550004011", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	source, err := users.Create(ctx, domain.User{AccessHash: 52, Phone: "15550004012", FirstName: "Source"})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	channels := appchannels.NewService(memory.NewChannelStore())
	created, err := channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Forward Replay",
		Megagroup:     true,
		Date:          1700001210,
	})
	if err != nil {
		t.Fatalf("create target channel: %v", err)
	}
	messages := &captureMessages{
		getMessagesListed: true,
		list: domain.MessageList{Messages: []domain.Message{
			{ID: 7, OwnerUserID: owner.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: source.ID}, From: domain.Peer{Type: domain.PeerTypeUser, ID: source.ID}, Date: 1700001207, Body: "seven"},
			{ID: 5, OwnerUserID: owner.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: source.ID}, From: domain.Peer{Type: domain.PeerTypeUser, ID: source.ID}, Date: 1700001205, Body: "five"},
		}},
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(users),
		Messages: messages,
		Channels: channels,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	reqCtx := WithSessionID(WithRawAuthKeyID(WithUserID(ctx, owner.ID), [8]byte{1}), 71)
	to := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	from := &tg.InputPeerUser{UserID: source.ID, AccessHash: source.AccessHash}

	firstReq := &tg.MessagesForwardMessagesRequest{FromPeer: from, ToPeer: to, ID: []int{7}, RandomID: []int64{7001}}
	if _, err := r.onMessagesForwardMessages(reqCtx, firstReq); err != nil {
		t.Fatalf("first forward: %v", err)
	}
	firstPushes := len(sessions.pushedUserIDs())
	if firstPushes == 0 {
		t.Fatal("first forward produced no realtime payload")
	}

	if _, err := r.onMessagesForwardMessages(reqCtx, firstReq); err != nil {
		t.Fatalf("full replay: %v", err)
	}
	if got := len(sessions.pushedUserIDs()); got != firstPushes {
		t.Fatalf("full replay realtime pushes = %d, want unchanged %d", got, firstPushes)
	}

	mixedReq := &tg.MessagesForwardMessagesRequest{
		FromPeer: from,
		ToPeer:   to,
		ID:       []int{7, 5},
		RandomID: []int64{7001, 5001},
	}
	if _, err := r.onMessagesForwardMessages(reqCtx, mixedReq); err != nil {
		t.Fatalf("mixed replay: %v", err)
	}
	if got := len(sessions.pushedUserIDs()); got != firstPushes+1 {
		t.Fatalf("mixed replay realtime pushes = %d, want %d", got, firstPushes+1)
	}
	updates, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok {
		t.Fatalf("mixed replay realtime payload = %T, want *tg.Updates", sessions.lastUserPush())
	}
	newMessages := 0
	for _, update := range updates.Updates {
		if _, ok := update.(*tg.UpdateNewChannelMessage); ok {
			newMessages++
		}
	}
	if newMessages != 1 {
		t.Fatalf("mixed replay realtime new-message updates = %d, want only newly committed item", newMessages)
	}
}

func TestMessagesForwardMessagesInfersPrivateSourceFromInputPeerEmpty(t *testing.T) {
	const (
		ownerID = int64(1780243210)
		fromID  = int64(1780243211)
		toID    = int64(1780243212)
	)
	ctx := context.Background()
	messages := &captureMessages{
		getMessagesListed: true,
		list: domain.MessageList{Messages: []domain.Message{
			{
				ID:          189,
				OwnerUserID: ownerID,
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: fromID},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: fromID},
				Date:        1700002189,
				Body:        "android source",
			},
		}},
	}
	r := New(Config{}, Deps{
		Messages: messages,
		Users: mapUsersService{users: map[int64]domain.User{
			ownerID: {ID: ownerID, FirstName: "Owner"},
			fromID:  {ID: fromID, FirstName: "From"},
			toID:    {ID: toID, FirstName: "To"},
		}},
	}, zaptest.NewLogger(t), clock.System)

	updatesClass, err := r.onMessagesForwardMessages(WithUserID(ctx, ownerID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerEmpty{},
		ToPeer:   &tg.InputPeerUser{UserID: toID},
		ID:       []int{189},
		RandomID: []int64{5069400637215652584},
	})
	if err != nil {
		t.Fatalf("forward with empty source peer: %v", err)
	}
	if messages.getMessagesCalls != 1 || len(messages.getMessagesIDs) != 1 || len(messages.getMessagesIDs[0]) != 1 || messages.getMessagesIDs[0][0] != 189 {
		t.Fatalf("GetMessages calls=%d ids=%+v, want one source lookup for [189]", messages.getMessagesCalls, messages.getMessagesIDs)
	}
	if messages.sendReq.RecipientUserID != toID || messages.sendReq.Message != "android source" {
		t.Fatalf("send request = %+v, want inferred source body to target", messages.sendReq)
	}
	if messages.sendReq.Forward == nil || messages.sendReq.Forward.From != (domain.Peer{Type: domain.PeerTypeUser, ID: fromID}) {
		t.Fatalf("forward header = %+v, want original author %d", messages.sendReq.Forward, fromID)
	}
	updates, ok := updatesClass.(*tg.Updates)
	if !ok || len(updates.Updates) != 2 {
		t.Fatalf("updates = %T %+v, want updateMessageID + updateNewMessage", updatesClass, updatesClass)
	}
	if id, ok := updates.Updates[0].(*tg.UpdateMessageID); !ok || id.RandomID != 5069400637215652584 {
		t.Fatalf("first update = %#v, want request random id", updates.Updates[0])
	}
}

func TestMessagesForwardMessagesInputPeerEmptyRejectsMixedPrivateSources(t *testing.T) {
	const (
		ownerID = int64(1780243210)
		fromA   = int64(1780243211)
		fromB   = int64(1780243212)
		toID    = int64(1780243213)
	)
	ctx := context.Background()
	messages := &captureMessages{
		getMessagesListed: true,
		list: domain.MessageList{Messages: []domain.Message{
			{ID: 10, OwnerUserID: ownerID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: fromA}, From: domain.Peer{Type: domain.PeerTypeUser, ID: fromA}, Date: 1700002210, Body: "first"},
			{ID: 11, OwnerUserID: ownerID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: fromB}, From: domain.Peer{Type: domain.PeerTypeUser, ID: fromB}, Date: 1700002211, Body: "second"},
		}},
	}
	r := New(Config{}, Deps{
		Messages: messages,
		Users: mapUsersService{users: map[int64]domain.User{
			ownerID: {ID: ownerID, FirstName: "Owner"},
			fromA:   {ID: fromA, FirstName: "FromA"},
			fromB:   {ID: fromB, FirstName: "FromB"},
			toID:    {ID: toID, FirstName: "To"},
		}},
	}, zaptest.NewLogger(t), clock.System)

	_, err := r.onMessagesForwardMessages(WithUserID(ctx, ownerID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerEmpty{},
		ToPeer:   &tg.InputPeerUser{UserID: toID},
		ID:       []int{10, 11},
		RandomID: []int64{10010, 10011},
	})
	if err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("forward mixed empty-source ids err = %v, want MESSAGE_ID_INVALID", err)
	}
	if messages.sendReq.RecipientUserID != 0 {
		t.Fatalf("send request = %+v, want no send after mixed source rejection", messages.sendReq)
	}
}

func TestMessagesForwardMessagesInputPeerEmptyRejectsBadIDsBeforeLookup(t *testing.T) {
	const ownerID = int64(1780243210)
	ctx := context.Background()
	messages := &captureMessages{}
	r := New(Config{}, Deps{
		Messages: messages,
	}, zaptest.NewLogger(t), clock.System)

	_, err := r.onMessagesForwardMessages(WithUserID(ctx, ownerID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerEmpty{},
		ToPeer:   &tg.InputPeerUser{UserID: 1780243211},
		ID:       []int{0},
		RandomID: []int64{10001},
	})
	if err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("forward bad empty-source id err = %v, want MESSAGE_ID_INVALID", err)
	}
	if messages.getMessagesCalls != 0 {
		t.Fatalf("GetMessages calls = %d, want no source lookup for invalid id", messages.getMessagesCalls)
	}
}

func TestMessagesForwardMessagesRejectsOtherNegativeTopMsgID(t *testing.T) {
	const ownerID = int64(1780243210)
	ctx := context.Background()
	messages := &captureMessages{}
	r := New(Config{}, Deps{
		Messages: messages,
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerUser{UserID: 1780243211},
		ToPeer:   &tg.InputPeerUser{UserID: 1780243212},
		ID:       []int{1},
		RandomID: []int64{10001},
	}
	req.SetTopMsgID(-2)

	_, err := r.onMessagesForwardMessages(WithUserID(ctx, ownerID), req)
	if err == nil || !strings.Contains(err.Error(), "REPLY_MESSAGE_ID_INVALID") {
		t.Fatalf("forward negative top_msg_id err = %v, want REPLY_MESSAGE_ID_INVALID", err)
	}
	if messages.getMessagesCalls != 0 {
		t.Fatalf("GetMessages calls = %d, want no source lookup for invalid top_msg_id", messages.getMessagesCalls)
	}
}

func TestMessagesForwardMessagesNormalizesAndroidDuplicateIDRetry(t *testing.T) {
	const (
		ownerID = int64(1780243210)
		fromID  = int64(1780243211)
	)
	ctx := context.Background()
	messages := &captureMessages{
		getMessagesListed: true,
		list: domain.MessageList{Messages: []domain.Message{
			{
				ID:          187,
				OwnerUserID: ownerID,
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: fromID},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: fromID},
				Date:        1700002187,
				Body:        "retry source",
			},
		}},
	}
	r := New(Config{}, Deps{
		Messages: messages,
		Users: mapUsersService{users: map[int64]domain.User{
			ownerID: {ID: ownerID, FirstName: "Owner"},
			fromID:  {ID: fromID, FirstName: "From"},
		}},
	}, zaptest.NewLogger(t), clock.System)

	updatesClass, err := r.onMessagesForwardMessages(WithUserID(ctx, ownerID), &tg.MessagesForwardMessagesRequest{
		FromPeer:   &tg.InputPeerEmpty{},
		ToPeer:     &tg.InputPeerUser{UserID: fromID},
		ID:         []int{187, 187},
		RandomID:   []int64{1993272996073519809},
		DropAuthor: true,
	})
	if err != nil {
		t.Fatalf("forward android duplicate-id retry: %v", err)
	}
	if messages.getMessagesCalls != 1 || len(messages.getMessagesIDs) != 1 || len(messages.getMessagesIDs[0]) != 1 || messages.getMessagesIDs[0][0] != 187 {
		t.Fatalf("GetMessages calls=%d ids=%+v, want one normalized source lookup for [187]", messages.getMessagesCalls, messages.getMessagesIDs)
	}
	if messages.sendReq.RecipientUserID != fromID || messages.sendReq.Message != "retry source" {
		t.Fatalf("send request = %+v, want one forwarded message to current peer", messages.sendReq)
	}
	if messages.sendReq.Forward != nil {
		t.Fatalf("forward header = %+v, want dropped author", messages.sendReq.Forward)
	}
	updates, ok := updatesClass.(*tg.Updates)
	if !ok || len(updates.Updates) != 2 {
		t.Fatalf("updates = %T %+v, want one updateMessageID + one updateNewMessage", updatesClass, updatesClass)
	}
	if id, ok := updates.Updates[0].(*tg.UpdateMessageID); !ok || id.RandomID != 1993272996073519809 {
		t.Fatalf("first update = %#v, want normalized random id", updates.Updates[0])
	}
}

func TestMessagesForwardMessagesRejectsUnpairedIDRandomVectors(t *testing.T) {
	const ownerID = int64(1780243210)
	ctx := context.Background()
	messages := &captureMessages{}
	r := New(Config{}, Deps{
		Messages: messages,
	}, zaptest.NewLogger(t), clock.System)

	_, err := r.onMessagesForwardMessages(WithUserID(ctx, ownerID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerEmpty{},
		ToPeer:   &tg.InputPeerUser{UserID: 1780243211},
		ID:       []int{187, 188},
		RandomID: []int64{1993272996073519809},
	})
	if err == nil || !strings.Contains(err.Error(), "INPUT_REQUEST_INVALID") {
		t.Fatalf("forward unpaired vectors err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if messages.getMessagesCalls != 0 {
		t.Fatalf("GetMessages calls = %d, want no source lookup for unpaired vectors", messages.getMessagesCalls)
	}
}

func TestChatsForMessageUpdatesUsesBatchChannelProjection(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{
		AccessHash: 51,
		Phone:      "15550005001",
		FirstName:  "BatchOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	first, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Message Ref One",
		Broadcast:     true,
		Date:          1700001500,
	})
	if err != nil {
		t.Fatalf("create first channel: %v", err)
	}
	second, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Message Ref Two",
		Megagroup:     true,
		Date:          1700001510,
	})
	if err != nil {
		t.Fatalf("create second channel: %v", err)
	}
	counting := &countingChannelsService{Service: channelService}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: counting,
	}, zaptest.NewLogger(t), clock.System)

	chats := r.chatsForMessageUpdates(ctx, owner.ID, []domain.Message{
		{
			From: domain.Peer{Type: domain.PeerTypeChannel, ID: first.Channel.ID},
			Peer: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
			Forward: &domain.MessageForward{
				From: domain.Peer{Type: domain.PeerTypeChannel, ID: second.Channel.ID},
			},
		},
		{
			Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: first.Channel.ID},
			ReplyTo: &domain.MessageReply{
				Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: second.Channel.ID},
			},
		},
	})
	if len(chats) != 2 {
		t.Fatalf("chats = %d, want two unique channel refs", len(chats))
	}
	firstChat, ok := chats[0].(*tg.Channel)
	if !ok || firstChat.ID != first.Channel.ID {
		t.Fatalf("first chat = %#v, want first channel", chats[0])
	}
	secondChat, ok := chats[1].(*tg.Channel)
	if !ok || secondChat.ID != second.Channel.ID {
		t.Fatalf("second chat = %#v, want second channel", chats[1])
	}
	if counting.getChannelsCalls != 1 || counting.getChannelCalls != 0 {
		t.Fatalf("channel service calls: GetChannels=%d GetChannel=%d, want one batch call only", counting.getChannelsCalls, counting.getChannelCalls)
	}
}

func TestMessagesForwardMessagesUnsupportedOptionErrors(t *testing.T) {
	const ownerID = int64(1000000001)
	ctx := WithUserID(context.Background(), ownerID)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	base := func() *tg.MessagesForwardMessagesRequest {
		return &tg.MessagesForwardMessagesRequest{
			FromPeer: &tg.InputPeerUser{UserID: 1000000002},
			ToPeer:   &tg.InputPeerUser{UserID: 1000000003},
			ID:       []int{3},
			RandomID: []int64{1001},
		}
	}
	suggested := func() tg.SuggestedPost {
		post := tg.SuggestedPost{}
		post.SetAccepted(true)
		return post
	}
	cases := []struct {
		name string
		req  *tg.MessagesForwardMessagesRequest
		want string
	}{
		{
			name: "quick reply",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetQuickReplyShortcut(&tg.InputQuickReplyShortcut{Shortcut: "hello"})
				return req
			}(),
			want: "SHORTCUT_INVALID",
		},
		{
			name: "effect",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetEffect(1)
				return req
			}(),
			want: "EFFECT_ID_INVALID",
		},
		{
			name: "video timestamp without media model",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetVideoTimestamp(10)
				return req
			}(),
			want: "MEDIA_INVALID",
		},
		{
			name: "negative paid stars",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetAllowPaidStars(-1)
				return req
			}(),
			want: "STARS_AMOUNT_INVALID",
		},
		{
			name: "paid floodskip",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetAllowPaidFloodskip(true)
				return req
			}(),
			want: "PAYMENT_UNSUPPORTED",
		},
		{
			name: "suggested post",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetSuggestedPost(suggested())
				return req
			}(),
			want: "SUGGESTED_POST_PEER_INVALID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onMessagesForwardMessages(ctx, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("forward err = %v, want %s", err, tc.want)
			}
		})
	}
}
