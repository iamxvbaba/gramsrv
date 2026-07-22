package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestMonoforumSavedDialogsAndHistory 验证频道私信(monoforum)读侧 RPC：管理员经
// getSavedDialogs/getSavedHistory 看订阅者子会话，parent_peer 同时兼容 TDesktop 实际发送的
// 母广播频道和虚拟 monoforum；订阅者经普通 getHistory 只看自己的子会话。
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
	parentInput := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

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
	var subscriberRaw bin.Buffer
	if err := (&tg.MessagesGetHistoryRequest{Peer: monoInput, Limit: 20}).Encode(&subscriberRaw); err != nil {
		t.Fatalf("encode non-admin getHistory(monoforum): %v", err)
	}
	subscriberEnc, err := r.Dispatch(WithUserID(ctx, sub.ID), [8]byte{}, 0, &subscriberRaw)
	if err != nil {
		t.Fatalf("non-admin getHistory(monoforum): %v", err)
	}
	subscriberHistory, ok := subscriberEnc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("non-admin getHistory(monoforum) = %T, want *tg.MessagesChannelMessages", subscriberEnc)
	}
	if len(subscriberHistory.Messages) != 1 {
		t.Fatalf("non-admin getHistory(monoforum) = %d msgs, want own sublist message", len(subscriberHistory.Messages))
	}
	subscriberMessage, ok := subscriberHistory.Messages[0].(*tg.Message)
	if !ok || subscriberMessage.Message != "hello channel" {
		t.Fatalf("non-admin history[0] = %#v, want own 'hello channel'", subscriberHistory.Messages[0])
	}
	subscriberSavedPeer, ok := subscriberMessage.GetSavedPeerID()
	if !ok {
		t.Fatalf("non-admin history message missing saved_peer_id")
	}
	if peer, ok := subscriberSavedPeer.(*tg.PeerUser); !ok || peer.UserID != sub.ID {
		t.Fatalf("non-admin history saved_peer_id = %#v, want self %d", subscriberSavedPeer, sub.ID)
	}

	// 管理员看私信列表。
	dreq := &tg.MessagesGetSavedDialogsRequest{}
	// TDesktop SavedSublist::loadAround() 的 parentChat()->input() 是母广播频道。
	dreq.SetParentPeer(parentInput)
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
	hreq.SetParentPeer(parentInput)
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

	// 虚拟 monoforum peer 仍是合法的等价入口，两个 parent 不能落到不同数据集。
	directMonoReq := &tg.MessagesGetSavedHistoryRequest{Peer: &tg.InputPeerUser{UserID: sub.ID}}
	directMonoReq.SetParentPeer(monoInput)
	directMonoRes, err := r.onMessagesGetSavedHistory(WithUserID(ctx, owner.ID), directMonoReq)
	if err != nil {
		t.Fatalf("getSavedHistory(direct monoforum): %v", err)
	}
	directMonoSlice, ok := directMonoRes.(*tg.MessagesMessagesSlice)
	if !ok || len(directMonoSlice.Messages) != 1 {
		t.Fatalf("getSavedHistory(direct monoforum) = %#v, want same single-message topic", directMonoRes)
	}

	// 非管理员(订阅者本人)经管理员入口看列表被拒。
	if _, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, sub.ID), dreq); err == nil {
		t.Fatalf("non-admin getSavedDialogs(monoforum) = nil err, want denied")
	}
}

// TestMonoforumSendMessageWritePath 验证写侧:订阅者按 TDesktop 实际请求仅以
// peer=monoforum 发到自己的子会话;管理员必须显式指定目标订阅者;suggested_post 被持久化返回;
// 订阅者不能写他人子会话。
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
	dialogSvc := appdialogs.NewService(memory.NewDialogStore(), channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
		Dialogs:  dialogSvc,
	}, zaptest.NewLogger(t), clock.System)

	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "DM Broadcast", Broadcast: true, Date: 1000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	enabled, err := channelStore.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 10, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	mono, err := channelStore.GetChannelByID(ctx, monoID)
	if err != nil {
		t.Fatalf("get monoforum: %v", err)
	}
	monoInput := &tg.InputPeerChannel{ChannelID: monoID, AccessHash: mono.AccessHash}
	monoChannelInput := &tg.InputChannel{ChannelID: monoID, AccessHash: mono.AccessHash}

	// 订阅者不是 monoforum 成员，但 TDesktop 打开会话时必须能读取 full channel shell。
	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, sub.ID), monoChannelInput)
	if err != nil {
		t.Fatalf("subscriber getFullChannel(monoforum): %v", err)
	}
	if full == nil || full.FullChat == nil {
		t.Fatalf("subscriber getFullChannel(monoforum) = %#v, want full chat", full)
	}

	// monoforum 永远不能通过 join 变成普通频道成员，否则会生成错误的 joined service message。
	if _, err := r.onChannelsJoinChannel(WithUserID(ctx, sub.ID), monoChannelInput); err == nil || !strings.Contains(err.Error(), "CHANNEL_MONOFORUM_UNSUPPORTED") {
		t.Fatalf("subscriber joinChannel(monoforum) err = %v, want CHANNEL_MONOFORUM_UNSUPPORTED", err)
	}

	// TDesktop 在发送前保存相同 suggested_post 草稿；它必须可写、可恢复，不能变成 CHANNEL_PRIVATE。
	draftSuggested := tg.SuggestedPost{}
	draftSuggested.SetPrice(&tg.StarsAmount{Amount: 10})
	draftSuggested.SetScheduleDate(1_700_100_000)
	draftReq := &tg.MessagesSaveDraftRequest{Peer: monoInput, Message: "pending suggested post"}
	draftReq.SetSuggestedPost(draftSuggested)
	if ok, err := r.onMessagesSaveDraft(WithUserID(ctx, sub.ID), draftReq); err != nil || !ok {
		t.Fatalf("subscriber saveDraft(monoforum) = %v, %v; want true, nil", ok, err)
	}
	storedDraft, found, err := dialogSvc.GetDraft(ctx, sub.ID, domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}, 0)
	if err != nil || !found {
		t.Fatalf("get persisted monoforum draft = %+v, %v, %v; want found", storedDraft, found, err)
	}
	if storedDraft.Message != "pending suggested post" || storedDraft.SuggestedPost == nil || storedDraft.SuggestedPost.Price == nil || storedDraft.SuggestedPost.Price.Amount != 10 || storedDraft.SuggestedPost.ScheduleDate != 1_700_100_000 {
		t.Fatalf("persisted monoforum draft = %+v, want suggested post content", storedDraft)
	}
	tooLow := &tg.MessagesSendMessageRequest{Peer: monoInput, Message: "under-authorized", RandomID: 554}
	tooLow.SetAllowPaidStars(9)
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, sub.ID), tooLow); err == nil || !strings.Contains(err.Error(), "ALLOW_PAYMENT_REQUIRED") || !strings.Contains(err.Error(), "(10)") {
		t.Fatalf("under-authorized paid message err = %v, want ALLOW_PAYMENT_REQUIRED_10", err)
	}

	// TDesktop 的订阅者请求不携带 InputReplyToMonoForum;服务端必须从调用者推导 saved_peer=self。
	subReq := &tg.MessagesSendMessageRequest{Peer: monoInput, Message: "hi from sub", RandomID: 555}
	subReq.ClearDraft = true
	subReq.SetAllowPaidStars(20)
	suggestedInput := tg.SuggestedPost{}
	suggestedInput.SetPrice(&tg.StarsAmount{Amount: 10})
	suggestedInput.SetScheduleDate(1_700_100_000)
	subReq.SetSuggestedPost(suggestedInput)
	subUpd, err := r.onMessagesSendMessage(WithUserID(ctx, sub.ID), subReq)
	if err != nil {
		t.Fatalf("subscriber sendMessage(monoforum): %v", err)
	}
	subUpdates, ok := subUpd.(*tg.Updates)
	if !ok {
		t.Fatalf("subscriber send updates = %T, want *tg.Updates", subUpd)
	}
	var subMessageID int
	var subPaidStars, subBalance int64
	for _, update := range subUpdates.Updates {
		if newMessage, ok := update.(*tg.UpdateNewChannelMessage); ok {
			if message, ok := newMessage.Message.(*tg.Message); ok {
				subMessageID = message.ID
				subPaidStars, _ = message.GetPaidMessageStars()
			}
		}
		if balance, ok := update.(*tg.UpdateStarsBalance); ok {
			if amount, ok := balance.Balance.(*tg.StarsAmount); ok {
				subBalance = amount.Amount
			}
		}
	}
	if subMessageID == 0 || subPaidStars != 10 || subBalance != 990 {
		t.Fatalf("subscriber send updates id/paid/balance = %d/%d/%d, want id>0/10/990: %#v", subMessageID, subPaidStars, subBalance, subUpdates.Updates)
	}
	if _, found, err := dialogSvc.GetDraft(ctx, sub.ID, domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}, 0); err != nil || found {
		t.Fatalf("clear_draft after paid send found/err = %v/%v, want false/nil", found, err)
	}
	duplicateUpd, err := r.onMessagesSendMessage(WithUserID(ctx, sub.ID), subReq)
	if err != nil {
		t.Fatalf("subscriber paid replay: %v", err)
	}
	duplicateUpdates, ok := duplicateUpd.(*tg.Updates)
	if !ok {
		t.Fatalf("subscriber paid replay = %T, want *tg.Updates", duplicateUpd)
	}
	var duplicateBalance int64
	for _, update := range duplicateUpdates.Updates {
		if balance, ok := update.(*tg.UpdateStarsBalance); ok {
			if amount, ok := balance.Balance.(*tg.StarsAmount); ok {
				duplicateBalance = amount.Amount
			}
		}
	}
	if duplicateBalance != 990 {
		t.Fatalf("subscriber paid replay balance = %d, want 990 without a second debit", duplicateBalance)
	}

	// 管理员回复到该订阅者的子会话：同一个 inputReplyToMessage 同时携带真实 reply id
	// 和 monoforum target，两部分都必须保留。
	adminReq := &tg.MessagesSendMessageRequest{Peer: monoInput, Message: "admin reply", RandomID: 556}
	adminReply := &tg.InputReplyToMessage{ReplyToMsgID: subMessageID}
	adminReply.SetMonoforumPeerID(&tg.InputPeerUser{UserID: sub.ID})
	adminReq.SetReplyTo(adminReply)
	adminReq.SetAllowPaidStars(100)
	adminUpd, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), adminReq)
	if err != nil {
		t.Fatalf("admin reply sendMessage(monoforum): %v", err)
	}
	if updates, ok := adminUpd.(*tg.Updates); ok {
		for _, update := range updates.Updates {
			if _, balance := update.(*tg.UpdateStarsBalance); balance {
				t.Fatalf("admin free reply emitted a balance debit: %#v", updates.Updates)
			}
			if newMessage, ok := update.(*tg.UpdateNewChannelMessage); ok {
				if message, ok := newMessage.Message.(*tg.Message); ok {
					if stars, paid := message.GetPaidMessageStars(); paid || stars != 0 {
						t.Fatalf("admin reply paid_message_stars = %d/%v, want 0/false", stars, paid)
					}
				}
			}
		}
	}

	// 带媒体的 suggested post 必须走同一 monoforum 子会话，不能退化为普通频道消息或丢 flags。
	mediaSuggested := tg.SuggestedPost{}
	mediaSuggested.SetPrice(&tg.StarsAmount{Amount: 15})
	mediaReq := &tg.MessagesSendMediaRequest{
		Peer: monoInput, RandomID: 558, Message: "media suggestion",
		Media: &tg.InputMediaContact{PhoneNumber: "+15550003003", FirstName: "Media", LastName: "Contact", Vcard: ""},
	}
	mediaReq.SetSuggestedPost(mediaSuggested)
	mediaReq.SetAllowPaidStars(15)
	if _, err := r.onMessagesSendMedia(WithUserID(ctx, sub.ID), mediaReq); err != nil {
		t.Fatalf("subscriber sendMedia(monoforum): %v", err)
	}

	// 订阅者不能写他人(owner)的子会话。
	sneaky := &tg.MessagesSendMessageRequest{Peer: monoInput, Message: "sneaky", RandomID: 557}
	sneaky.SetReplyTo(&tg.InputReplyToMonoForum{MonoforumPeerID: &tg.InputPeerUser{UserID: owner.ID}})
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, sub.ID), sneaky); err == nil {
		t.Fatalf("subscriber writing another's sublist = nil err, want REPLY_TO_MONOFORUM_PEER_INVALID")
	}

	// 经管理员读历史:子会话含三条(订阅者文本 + 管理员回复 + 订阅者媒体),倒序。
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
	if len(slice.Messages) != 3 {
		t.Fatalf("history = %d msgs, want 3 (sub text + admin + sub media)", len(slice.Messages))
	}
	top, ok := slice.Messages[0].(*tg.Message)
	if !ok || top.Message != "media suggestion" {
		t.Fatalf("history[0] = %#v, want newest media suggestion", slice.Messages[0])
	}
	if _, ok := top.Media.(*tg.MessageMediaContact); !ok {
		t.Fatalf("history[0] media = %T, want MessageMediaContact", top.Media)
	}
	if paid, ok := top.GetPaidMessageStars(); !ok || paid != 10 {
		t.Fatalf("media paid_message_stars = %d/%v, want actual configured price 10", paid, ok)
	}
	topSuggested, ok := top.GetSuggestedPost()
	if !ok {
		t.Fatalf("media message missing suggested_post")
	}
	topPrice, ok := topSuggested.GetPrice()
	if !ok {
		t.Fatalf("media suggested_post missing price")
	}
	if stars, ok := topPrice.(*tg.StarsAmount); !ok || stars.Amount != 15 {
		t.Fatalf("media suggested_post price = %#v, want 15 Stars", topPrice)
	}
	adminMessage, ok := slice.Messages[1].(*tg.Message)
	if !ok || adminMessage.Message != "admin reply" {
		t.Fatalf("history[1] = %#v, want admin reply", slice.Messages[1])
	}
	if header, ok := adminMessage.ReplyTo.(*tg.MessageReplyHeader); !ok || header.ReplyToMsgID != subMessageID {
		t.Fatalf("admin reply header = %#v, want reply_to_msg_id %d", adminMessage.ReplyTo, subMessageID)
	}
	suggestedMessage, ok := slice.Messages[2].(*tg.Message)
	if !ok {
		t.Fatalf("history[2] = %T, want *tg.Message", slice.Messages[2])
	}
	suggested, ok := suggestedMessage.GetSuggestedPost()
	if !ok {
		t.Fatalf("subscriber message missing suggested_post")
	}
	price, ok := suggested.GetPrice()
	if !ok {
		t.Fatalf("suggested_post missing price")
	}
	stars, ok := price.(*tg.StarsAmount)
	if !ok || stars.Amount != 10 {
		t.Fatalf("suggested_post price = %#v, want 10 Stars", price)
	}
	if scheduleDate, ok := suggested.GetScheduleDate(); !ok || scheduleDate != 1_700_100_000 {
		t.Fatalf("suggested_post schedule = %d/%v, want 1700100000/true", scheduleDate, ok)
	}
}
