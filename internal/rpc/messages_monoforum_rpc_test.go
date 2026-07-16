package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestMonoforumSavedDialogsAndHistory 验证频道私信(monoforum)读侧 RPC:管理员经
// getSavedDialogs(parent_peer=monoforum) 看订阅者子会话列表、经 getSavedHistory 看某订阅者历史
// (消息带 saved_peer_id);非管理员被拒。
func TestMonoforumSavedDialogsAndHistory(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550002001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sub, err := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550002002", FirstName: "Sub"})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}

	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "DM Broadcast", Broadcast: true, Date: 1000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	enabled, err := channelStore.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	if monoID == 0 {
		t.Fatalf("no monoforum created")
	}

	subPeer := domain.Peer{Type: domain.PeerTypeUser, ID: sub.ID}
	if _, err := channelStore.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 1, Message: "hello channel", Date: 1100,
	}); err != nil {
		t.Fatalf("seed monoforum message: %v", err)
	}

	mono, err := channelStore.GetChannelByID(ctx, monoID)
	if err != nil {
		t.Fatalf("get monoforum: %v", err)
	}
	monoInput := &tg.InputPeerChannel{ChannelID: monoID, AccessHash: mono.AccessHash}

	// TDesktop 点 Direct Messages 入口会先按 monoforum peer 拉普通 channel history。
	// 主历史只应返回 monoforum 自身的 service messages,不能混入 saved_peer 子会话消息。
	var raw bin.Buffer
	if err := (&tg.MessagesGetHistoryRequest{Peer: monoInput, Limit: 20}).Encode(&raw); err != nil {
		t.Fatalf("encode getHistory(monoforum): %v", err)
	}
	mainEnc, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &raw)
	if err != nil {
		t.Fatalf("dispatch getHistory(monoforum): %v", err)
	}
	mainHistory, ok := mainEnc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("getHistory(monoforum) = %T, want *tg.MessagesChannelMessages", mainEnc)
	}
	if len(mainHistory.Messages) != 1 {
		t.Fatalf("main monoforum history = %d msgs, want only the creation service", len(mainHistory.Messages))
	}
	service, ok := mainHistory.Messages[0].(*tg.MessageService)
	if !ok {
		t.Fatalf("main monoforum message = %T, want MessageService", mainHistory.Messages[0])
	}
	// monoforum 的首条服务消息是创建消息;TDesktop 对 monoforum 渲染为 "Direct messages were
	// enabled in this channel."(lng_action_created_monoforum)。paid_messages_price 只进母广播频道。
	if _, ok := service.Action.(*tg.MessageActionChannelCreate); !ok {
		t.Fatalf("main monoforum action = %T %+v, want MessageActionChannelCreate", service.Action, service.Action)
	}
	seenChats := map[int64]bool{}
	for _, chat := range mainHistory.Chats {
		if ch, ok := chat.(*tg.Channel); ok {
			seenChats[ch.ID] = true
		}
	}
	if !seenChats[monoID] || !seenChats[created.Channel.ID] {
		t.Fatalf("main monoforum chats = %+v, want monoforum %d and parent %d", seenChats, monoID, created.Channel.ID)
	}
	var deniedRaw bin.Buffer
	if err := (&tg.MessagesGetHistoryRequest{Peer: monoInput, Limit: 20}).Encode(&deniedRaw); err != nil {
		t.Fatalf("encode non-admin getHistory(monoforum): %v", err)
	}
	if _, err := r.Dispatch(WithUserID(ctx, sub.ID), [8]byte{}, 0, &deniedRaw); err == nil {
		t.Fatalf("non-admin getHistory(monoforum) = nil err, want denied")
	}

	// 管理员看私信列表。
	dreq := &tg.MessagesGetSavedDialogsRequest{}
	dreq.SetParentPeer(monoInput)
	dres, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, owner.ID), dreq)
	if err != nil {
		t.Fatalf("getSavedDialogs(monoforum): %v", err)
	}
	sd, ok := dres.(*tg.MessagesSavedDialogs)
	if !ok {
		t.Fatalf("getSavedDialogs = %T, want *tg.MessagesSavedDialogs", dres)
	}
	if len(sd.Dialogs) != 1 {
		t.Fatalf("dialogs = %d, want 1 subscriber sublist", len(sd.Dialogs))
	}
	md, ok := sd.Dialogs[0].(*tg.MonoForumDialog)
	if !ok {
		t.Fatalf("dialog = %T, want *tg.MonoForumDialog", sd.Dialogs[0])
	}
	if pu, ok := md.Peer.(*tg.PeerUser); !ok || pu.UserID != sub.ID {
		t.Fatalf("dialog peer = %#v, want PeerUser %d", md.Peer, sub.ID)
	}
	foundSub := false
	for _, u := range sd.Users {
		if usr, ok := u.(*tg.User); ok && usr.ID == sub.ID {
			foundSub = true
		}
	}
	if !foundSub {
		t.Fatalf("getSavedDialogs users missing subscriber %d", sub.ID)
	}

	// 管理员看某订阅者会话历史。
	hreq := &tg.MessagesGetSavedHistoryRequest{Peer: &tg.InputPeerUser{UserID: sub.ID}}
	hreq.SetParentPeer(monoInput)
	hres, err := r.onMessagesGetSavedHistory(WithUserID(ctx, owner.ID), hreq)
	if err != nil {
		t.Fatalf("getSavedHistory(monoforum): %v", err)
	}
	var gotMsgs []tg.MessageClass
	switch m := hres.(type) {
	case *tg.MessagesMessages:
		gotMsgs = m.Messages
	case *tg.MessagesMessagesSlice:
		gotMsgs = m.Messages
	case *tg.MessagesChannelMessages:
		gotMsgs = m.Messages
	default:
		t.Fatalf("getSavedHistory = %T, want messages", hres)
	}
	if len(gotMsgs) != 1 {
		t.Fatalf("history = %d msgs, want 1", len(gotMsgs))
	}
	msg, ok := gotMsgs[0].(*tg.Message)
	if !ok {
		t.Fatalf("msg = %T, want *tg.Message", gotMsgs[0])
	}
	if msg.Message != "hello channel" {
		t.Fatalf("body = %q, want 'hello channel'", msg.Message)
	}
	sp, ok := msg.GetSavedPeerID()
	if !ok {
		t.Fatalf("message missing saved_peer_id (client can't group into subscriber sublist)")
	}
	if pu, ok := sp.(*tg.PeerUser); !ok || pu.UserID != sub.ID {
		t.Fatalf("saved_peer_id = %#v, want sub %d", sp, sub.ID)
	}

	// 非管理员(订阅者本人)经管理员入口看列表被拒。
	if _, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, sub.ID), dreq); err == nil {
		t.Fatalf("non-admin getSavedDialogs(monoforum) = nil err, want denied")
	}
}

// TestMonoforumSendMessageWritePath 验证写侧:订阅者经 sendMessage(peer=monoforum,
// reply_to=InputReplyToMonoForum{自己}) 发私信;管理员回复到目标订阅者;订阅者不能写他人子会话;
// 普通发送(无 monoforum_peer_id)不受影响。
func TestMonoforumSendMessageWritePath(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 21, Phone: "15550003001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sub, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550003002", FirstName: "Sub"})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}

	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "DM Broadcast", Broadcast: true, Date: 1000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	enabled, err := channelStore.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	monoInput := &tg.InputPeerChannel{ChannelID: monoID}

	// 订阅者发私信到自己的子会话。
	subReq := &tg.MessagesSendMessageRequest{Peer: monoInput, Message: "hi from sub", RandomID: 555}
	subReq.SetReplyTo(&tg.InputReplyToMonoForum{MonoforumPeerID: &tg.InputPeerUser{UserID: sub.ID}})
	subUpd, err := r.onMessagesSendMessage(WithUserID(ctx, sub.ID), subReq)
	if err != nil {
		t.Fatalf("subscriber sendMessage(monoforum): %v", err)
	}
	if _, ok := subUpd.(*tg.Updates); !ok {
		t.Fatalf("subscriber send updates = %T, want *tg.Updates", subUpd)
	}

	// 管理员回复到该订阅者的子会话。
	adminReq := &tg.MessagesSendMessageRequest{Peer: monoInput, Message: "admin reply", RandomID: 556}
	adminReq.SetReplyTo(&tg.InputReplyToMonoForum{MonoforumPeerID: &tg.InputPeerUser{UserID: sub.ID}})
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), adminReq); err != nil {
		t.Fatalf("admin reply sendMessage(monoforum): %v", err)
	}

	// 订阅者不能写他人(owner)的子会话。
	sneaky := &tg.MessagesSendMessageRequest{Peer: monoInput, Message: "sneaky", RandomID: 557}
	sneaky.SetReplyTo(&tg.InputReplyToMonoForum{MonoforumPeerID: &tg.InputPeerUser{UserID: owner.ID}})
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, sub.ID), sneaky); err == nil {
		t.Fatalf("subscriber writing another's sublist = nil err, want REPLY_TO_MONOFORUM_PEER_INVALID")
	}

	// 经管理员读历史:子会话含两条(订阅者发 + 管理员回复),倒序。
	hreq := &tg.MessagesGetSavedHistoryRequest{Peer: &tg.InputPeerUser{UserID: sub.ID}}
	hreq.SetParentPeer(monoInput)
	hres, err := r.onMessagesGetSavedHistory(WithUserID(ctx, owner.ID), hreq)
	if err != nil {
		t.Fatalf("getSavedHistory after sends: %v", err)
	}
	slice, ok := hres.(*tg.MessagesMessagesSlice)
	if !ok {
		t.Fatalf("getSavedHistory = %T, want *tg.MessagesMessagesSlice", hres)
	}
	if len(slice.Messages) != 2 {
		t.Fatalf("history = %d msgs, want 2 (sub + admin)", len(slice.Messages))
	}
	top, ok := slice.Messages[0].(*tg.Message)
	if !ok || top.Message != "admin reply" {
		t.Fatalf("history[0] = %#v, want newest 'admin reply'", slice.Messages[0])
	}
}
