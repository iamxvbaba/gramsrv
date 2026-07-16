package rpc

import (
	"context"
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

// TestChannelMultiPinAndroidOpenAndJump 模拟 DrKLO Android 超级群多置顶消费链路：
// 打开聊天 → messages.search(filterPinned, limit=40, offset_id=0) 全量拉置顶列表；
// 点置顶栏跳最旧 pin → messages.getHistory(offset_id=pin, add_offset=-count/2, limit=count)
// AROUND 加载，响应必须包含锚点消息本身，否则客户端弹 MessageNotFound 放弃跳转；
// 本地缺对象 → channels.getMessages 精确补拉，messageEmpty 会被客户端丢弃。
func TestChannelMultiPinAndroidOpenAndJump(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550001161", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 62, Phone: "15550001162", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "MultiPin Android",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700001000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID

	const total = 30
	ids := make([]int, 0, total)
	for i := 0; i < total; i++ {
		sent, err := channelSvc.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
			ChannelID: channelID,
			RandomID:  int64(961000 + i),
			Message:   "msg",
			Date:      1700001001 + i,
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		ids = append(ids, sent.Message.ID)
	}
	// 三条置顶：早期、中间、最新（Android 置顶栏循环跳转需要全部三条都可跳）。
	pins := []int{ids[4], ids[14], ids[27]}
	for _, id := range pins {
		if _, err := channelSvc.UpdatePinnedMessage(ctx, owner.ID, domain.UpdateChannelPinnedMessageRequest{
			ChannelID: channelID,
			MessageID: id,
			Pinned:    true,
			Date:      1700001100,
		}); err != nil {
			t.Fatalf("pin %d: %v", id, err)
		}
	}

	memberView, err := channelSvc.GetChannel(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("member get channel: %v", err)
	}
	peer := &tg.InputPeerChannel{ChannelID: channelID, AccessHash: memberView.Channel.AccessHash}
	dispatch := func(req bin.Encoder) bin.Encoder {
		t.Helper()
		var b bin.Buffer
		if err := req.Encode(&b); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		enc, err := r.Dispatch(WithUserID(androidClientContext(), member.ID), [8]byte{}, 0, &b)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		return enc
	}

	// ① 打开聊天：MediaDataController.loadPinnedMessages → messages.search filterPinned。
	searchEnc := dispatch(&tg.MessagesSearchRequest{
		Peer:   peer,
		Q:      "",
		Filter: &tg.InputMessagesFilterPinned{},
		Limit:  40,
	})
	channelMessages, ok := searchEnc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("pinned search response = %T, want messages.channelMessages", searchEnc)
	}
	if len(channelMessages.Messages) != len(pins) {
		t.Fatalf("pinned search messages = %d, want %d", len(channelMessages.Messages), len(pins))
	}
	if channelMessages.Count != len(pins) {
		t.Fatalf("pinned search count = %d, want %d", channelMessages.Count, len(pins))
	}
	wantDesc := []int{pins[2], pins[1], pins[0]}
	for i, raw := range channelMessages.Messages {
		msg, ok := raw.(*tg.Message)
		if !ok {
			// TL_messageService / TL_messageEmpty 会被 Android loadPinnedMessages 直接跳过。
			t.Fatalf("pinned search message[%d] = %T, want *tg.Message", i, raw)
		}
		if msg.ID != wantDesc[i] {
			t.Fatalf("pinned search order[%d] = %d, want %d (id desc)", i, msg.ID, wantDesc[i])
		}
		if !msg.Pinned {
			t.Fatalf("pinned search message %d lacks pinned flag", msg.ID)
		}
	}

	// ② 点置顶栏跳最旧 pin：scrollToMessageId → getHistory AROUND（手机 count=20）。
	const aroundCount = 20
	histEnc := dispatch(&tg.MessagesGetHistoryRequest{
		Peer:      peer,
		OffsetID:  pins[0],
		AddOffset: -aroundCount / 2,
		Limit:     aroundCount,
	})
	histMessages, _, _ := searchMessagesPayload(t, histEnc)
	if len(histMessages) == 0 || len(histMessages) > aroundCount {
		t.Fatalf("around history size = %d, want 1..%d (超出 count 时 Android 会丢最新一条)", len(histMessages), aroundCount)
	}
	anchorFound := false
	lastID := int(^uint(0) >> 1)
	for _, raw := range histMessages {
		if _, isEmpty := raw.(*tg.MessageEmpty); isEmpty {
			t.Fatalf("around history contains messageEmpty")
		}
		id := raw.GetID()
		if id >= lastID {
			t.Fatalf("around history not id-desc: %d then %d", lastID, id)
		}
		lastID = id
		if id == pins[0] {
			anchorFound = true
		}
	}
	if !anchorFound {
		// ChatActivity postponedScroll 在响应缺锚点时直接 MessageNotFound 放弃跳转。
		t.Fatalf("around history lacks anchor %d: jump shows MessageNotFound on Android", pins[0])
	}

	// ③ 本地缺对象补拉：MessagesStorage.loadChatInfo → channels.getMessages。
	// DrKLO 发的是 pre-InputMessage 构造器 #93d7b347（id:Vector<int>），
	// 该请求 500 会让客户端把这批 pin 按「已取消置顶」从本地缓存删除。
	var legacy bin.Buffer
	legacy.PutID(0x93d7b347)
	if err := (&tg.InputChannel{ChannelID: channelID, AccessHash: memberView.Channel.AccessHash}).Encode(&legacy); err != nil {
		t.Fatalf("encode legacy input channel: %v", err)
	}
	legacy.PutVectorHeader(len(pins))
	for _, id := range pins {
		legacy.PutInt(id)
	}
	getEnc, err := r.Dispatch(WithUserID(androidClientContext(), member.ID), [8]byte{}, 0, &legacy)
	if err != nil {
		t.Fatalf("dispatch legacy channels.getMessages#93d7b347: %v", err)
	}
	getMessages, _, _ := searchMessagesPayload(t, getEnc)
	if len(getMessages) != len(pins) {
		t.Fatalf("legacy channels.getMessages size = %d, want %d", len(getMessages), len(pins))
	}
	for i, raw := range getMessages {
		msg, ok := raw.(*tg.Message)
		if !ok {
			t.Fatalf("legacy channels.getMessages[%d] = %T, want *tg.Message (messageEmpty 会被客户端丢弃)", i, raw)
		}
		if !msg.Pinned {
			t.Fatalf("legacy channels.getMessages message %d lacks pinned flag", msg.ID)
		}
	}
	// 新构造器（TDesktop 路径）必须与 legacy 返回一致的消息集合。
	getIDs := make([]tg.InputMessageClass, 0, len(pins))
	for _, id := range pins {
		getIDs = append(getIDs, &tg.InputMessageID{ID: id})
	}
	modernEnc := dispatch(&tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: channelID, AccessHash: memberView.Channel.AccessHash},
		ID:      getIDs,
	})
	modernMessages, _, _ := searchMessagesPayload(t, modernEnc)
	if len(modernMessages) != len(getMessages) {
		t.Fatalf("modern channels.getMessages size = %d, want %d (legacy/modern must match)", len(modernMessages), len(getMessages))
	}
	for i := range modernMessages {
		if modernMessages[i].GetID() != getMessages[i].GetID() {
			t.Fatalf("modern/legacy mismatch at %d: %d != %d", i, modernMessages[i].GetID(), getMessages[i].GetID())
		}
	}

	// ④ chatFull 降级缓存：pinned_msg_id 必须是最新置顶（Android 以它判断是否重拉列表）。
	fullEnc := dispatch(&tg.ChannelsGetFullChannelRequest{
		Channel: &tg.InputChannel{ChannelID: channelID, AccessHash: memberView.Channel.AccessHash},
	})
	full, ok := fullEnc.(*tg.MessagesChatFull)
	if !ok {
		t.Fatalf("getFullChannel response = %T, want messages.chatFull", fullEnc)
	}
	channelFull, ok := full.FullChat.(*tg.ChannelFull)
	if !ok {
		t.Fatalf("full chat = %T, want channelFull", full.FullChat)
	}
	if pinnedID, _ := channelFull.GetPinnedMsgID(); pinnedID != pins[2] {
		t.Fatalf("channelFull pinned_msg_id = %d, want latest pin %d", pinnedID, pins[2])
	}
}
