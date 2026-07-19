package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestMessagesSendWebViewDataServiceMessageRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	f.router.deps.BotAPIUpdates = memory.NewBotAPIUpdateStore()
	ownerCtx := WithUserID(ctx, f.owner.ID)

	updatesClass, err := f.router.onMessagesSendWebViewData(ownerCtx, &tg.MessagesSendWebViewDataRequest{
		Bot:        inputUser(f.bot),
		RandomID:   1001,
		ButtonText: "Open",
		Data:       `{"ok":true}`,
	})
	if err != nil {
		t.Fatalf("send webview data: %v", err)
	}
	updates := updatesClass.(*tg.Updates)
	if len(updates.Updates) != 1 {
		t.Fatalf("updates len = %d, want one service message", len(updates.Updates))
	}
	newMessage, ok := updates.Updates[0].(*tg.UpdateNewMessage)
	if !ok {
		t.Fatalf("update = %T, want UpdateNewMessage", updates.Updates[0])
	}
	service, ok := newMessage.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("message = %T, want MessageService", newMessage.Message)
	}
	sent, ok := service.Action.(*tg.MessageActionWebViewDataSent)
	if !ok || sent.Text != "Open" {
		t.Fatalf("user action = %+v (%T), want messageActionWebViewDataSent Open", service.Action, service.Action)
	}
	if service.PeerID.(*tg.PeerUser).UserID != f.bot.ID || service.FromID.(*tg.PeerUser).UserID != f.owner.ID {
		t.Fatalf("service peer/from = %+v/%+v, want bot/user", service.PeerID, service.FromID)
	}
	botAPIEvents, err := f.router.BotAPIUpdates(ctx, f.bot.ID, 0)
	if err != nil || len(botAPIEvents) != 1 || botAPIEvents[0].Message.Media == nil ||
		botAPIEvents[0].Message.Media.ServiceAction == nil ||
		botAPIEvents[0].Message.Media.ServiceAction.WebViewData == nil ||
		botAPIEvents[0].Message.Media.ServiceAction.WebViewData.Data != `{"ok":true}` {
		t.Fatalf("bot api webview events=%#v err=%v", botAPIEvents, err)
	}

	botHistory, err := f.router.deps.Messages.GetHistory(ctx, f.bot.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID},
		Limit:   10,
	})
	if err != nil || len(botHistory.Messages) != 1 {
		t.Fatalf("bot history = %+v err=%v, want one service message", botHistory, err)
	}
	botService, ok := tgMessage(botHistory.Messages[0]).(*tg.MessageService)
	if !ok {
		t.Fatalf("bot history tg message = %T, want MessageService", tgMessage(botHistory.Messages[0]))
	}
	sentMe, ok := botService.Action.(*tg.MessageActionWebViewDataSentMe)
	if !ok || sentMe.Text != "Open" || sentMe.Data != `{"ok":true}` {
		t.Fatalf("bot action = %+v (%T), want messageActionWebViewDataSentMe Open/data", botService.Action, botService.Action)
	}

	repeat, err := f.router.onMessagesSendWebViewData(ownerCtx, &tg.MessagesSendWebViewDataRequest{
		Bot:        inputUser(f.bot),
		RandomID:   1001,
		ButtonText: "Open",
		Data:       `{"ok":true}`,
	})
	if err != nil {
		t.Fatalf("repeat send webview data: %v", err)
	}
	repeatUpdates := repeat.(*tg.Updates).Updates
	if len(repeatUpdates) != 2 {
		t.Fatalf("repeat updates len = %d, want immutable mapping + message confirmation", len(repeatUpdates))
	}
	mapping, ok := repeatUpdates[0].(*tg.UpdateMessageID)
	if !ok || mapping.ID != service.ID || mapping.RandomID != 1001 {
		t.Fatalf("repeat mapping = %+v (%T), want random_id 1001 -> original %d", repeatUpdates[0], repeatUpdates[0], service.ID)
	}
	if replayed, ok := repeatUpdates[1].(*tg.UpdateNewMessage); !ok || replayed.Pts <= 0 || replayed.PtsCount != 1 {
		t.Fatalf("repeat confirmation = %T %+v, want UpdateNewMessage", repeatUpdates[1], repeatUpdates[1])
	}
	botHistory, err = f.router.deps.Messages.GetHistory(ctx, f.bot.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID},
		Limit:   10,
	})
	if err != nil || len(botHistory.Messages) != 1 {
		t.Fatalf("bot history after repeat = %+v err=%v, want still one service message", botHistory, err)
	}
	if _, err := f.router.onMessagesSendWebViewData(ownerCtx, &tg.MessagesSendWebViewDataRequest{
		Bot:        inputUser(f.bot),
		RandomID:   1001,
		ButtonText: "Open changed",
		Data:       `{"ok":false}`,
	}); !tgerr.Is(err, "RANDOM_ID_DUPLICATE") {
		t.Fatalf("conflicting webview random_id err = %v, want RANDOM_ID_DUPLICATE", err)
	}

	if _, err := f.router.onMessagesSendWebViewData(ownerCtx, &tg.MessagesSendWebViewDataRequest{
		Bot:        inputUser(f.peer),
		RandomID:   1002,
		ButtonText: "Open",
		Data:       "{}",
	}); !tgerr.Is(err, "BOT_INVALID") {
		t.Fatalf("send webview data non-bot err = %v, want BOT_INVALID", err)
	}
	if _, err := f.router.onMessagesSendWebViewData(ownerCtx, &tg.MessagesSendWebViewDataRequest{
		Bot:        inputUser(f.bot),
		RandomID:   1003,
		ButtonText: "Open",
		Data:       strings.Repeat("x", domain.MaxWebViewDataPayloadLen+1),
	}); !tgerr.Is(err, "BUTTON_DATA_INVALID") {
		t.Fatalf("send webview data too large err = %v, want BUTTON_DATA_INVALID", err)
	}
}

func TestMessagesSendBotRequestedPeerRejectsWithoutRequestButtonState(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)

	if _, err := f.router.onMessagesSendBotRequestedPeer(ownerCtx, &tg.MessagesSendBotRequestedPeerRequest{
		Peer:           inputPeerUser(f.bot),
		ButtonID:       1,
		RequestedPeers: []tg.InputPeerClass{inputPeerUser(f.peer)},
	}); !tgerr.Is(err, "BUTTON_DATA_INVALID") {
		t.Fatalf("send requested peer err = %v, want BUTTON_DATA_INVALID", err)
	}
	if _, err := f.router.onMessagesSendBotRequestedPeer(ownerCtx, &tg.MessagesSendBotRequestedPeerRequest{
		Peer:           inputPeerUser(f.bot),
		ButtonID:       1,
		RequestedPeers: nil,
	}); !tgerr.Is(err, "BUTTON_DATA_INVALID") {
		t.Fatalf("send requested peer without peers err = %v, want BUTTON_DATA_INVALID", err)
	}
}

func TestMessagesSendBotRequestedPeerQueuesBotAPIResponse(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	f.router.deps.BotAPIUpdates = memory.NewBotAPIUpdateStore()
	ownerCtx := WithUserID(ctx, f.owner.ID)
	button := domain.MarkupButton{
		Type: domain.MarkupButtonRequestPeer, Text: "Share user", ButtonID: 77,
		RequestPeerType: "user", MaxQuantity: 1, NameRequested: true, UsernameRequested: true,
	}
	requestMessage, err := f.router.deps.Messages.SendPrivateText(ctx, f.bot.ID, domain.SendPrivateTextRequest{
		SenderUserID: f.bot.ID, RecipientUserID: f.owner.ID, RandomID: 7001, Message: "Choose",
		ReplyMarkup: &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupKeyboard, Keyboard: [][]domain.MarkupButton{{button}}},
		Date:        1700000100,
	})
	if err != nil {
		t.Fatalf("send request message: %v", err)
	}
	if _, err := f.router.onMessagesSendBotRequestedPeer(ownerCtx, &tg.MessagesSendBotRequestedPeerRequest{
		Peer: inputPeerUser(f.bot), MsgID: requestMessage.RecipientMessage.ID, ButtonID: button.ButtonID,
		RequestedPeers: []tg.InputPeerClass{inputPeerUser(f.peer)},
	}); err != nil {
		t.Fatalf("send requested peer: %v", err)
	}
	events, err := f.router.BotAPIUpdates(ctx, f.bot.ID, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("bot api requested-peer events=%#v err=%v", events, err)
	}
	action := events[0].Message.Media.ServiceAction.RequestedPeer
	if action == nil || action.ButtonID != 77 || len(action.Peers) != 1 || action.Peers[0].ID != f.peer.ID ||
		len(action.Details) != 1 || action.Details[0].Peer != action.Peers[0] || action.Details[0].FirstName != f.peer.FirstName ||
		!action.NameRequested || !action.UsernameRequested {
		t.Fatalf("requested-peer action=%#v", action)
	}
}

func TestMessagesGetPreparedInlineMessageRejectsMissingRegistry(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)

	if _, err := f.router.onMessagesGetPreparedInlineMessage(ownerCtx, &tg.MessagesGetPreparedInlineMessageRequest{
		Bot: inputUser(f.bot),
		ID:  "prepared-1",
	}); !tgerr.Is(err, "RESULT_ID_INVALID") {
		t.Fatalf("get prepared inline err = %v, want RESULT_ID_INVALID", err)
	}
	if _, err := f.router.onMessagesGetPreparedInlineMessage(ownerCtx, &tg.MessagesGetPreparedInlineMessageRequest{
		Bot: inputUser(f.bot),
	}); !tgerr.Is(err, "RESULT_ID_EMPTY") {
		t.Fatalf("get prepared inline empty id err = %v, want RESULT_ID_EMPTY", err)
	}
	if _, err := f.router.onMessagesGetPreparedInlineMessage(ownerCtx, &tg.MessagesGetPreparedInlineMessageRequest{
		Bot: inputUser(f.peer),
		ID:  "prepared-1",
	}); !tgerr.Is(err, "BOT_INVALID") {
		t.Fatalf("get prepared inline non-bot err = %v, want BOT_INVALID", err)
	}
}

func TestMessagesPreparedInlineRoundTripSendsMessage(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	botCtx := WithUserID(ctx, f.bot.ID)
	ownerCtx := WithUserID(ctx, f.owner.ID)

	save := &tg.MessagesSavePreparedInlineMessageRequest{
		Result: preparedInlineArticleResult(),
		UserID: inputUser(f.owner),
	}
	save.SetPeerTypes([]tg.InlineQueryPeerTypeClass{&tg.InlineQueryPeerTypePM{}})
	prepared, err := f.router.onMessagesSavePreparedInlineMessage(botCtx, save)
	if err != nil {
		t.Fatalf("save prepared inline: %v", err)
	}
	got, err := f.router.onMessagesGetPreparedInlineMessage(ownerCtx, &tg.MessagesGetPreparedInlineMessageRequest{
		Bot: inputUser(f.bot),
		ID:  prepared.ID,
	})
	if err != nil {
		t.Fatalf("get prepared inline: %v", err)
	}
	if got.QueryID == 0 || got.CacheTime <= 0 || len(got.PeerTypes) != 1 {
		t.Fatalf("prepared inline = query %d cache %d peer_types %d", got.QueryID, got.CacheTime, len(got.PeerTypes))
	}
	if _, ok := got.PeerTypes[0].(*tg.InlineQueryPeerTypePM); !ok {
		t.Fatalf("prepared peer type = %T, want PM", got.PeerTypes[0])
	}
	result, ok := got.Result.(*tg.BotInlineResult)
	if !ok || result.ID != "prepared-article" {
		t.Fatalf("prepared result = %T/%#v, want prepared article", got.Result, got.Result)
	}
	if len(got.Users) != 1 {
		t.Fatalf("prepared users = %d, want bot user", len(got.Users))
	}

	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 9931,
		QueryID:  got.QueryID,
		ID:       result.ID,
	})
	if err != nil {
		t.Fatalf("send prepared inline result: %v", err)
	}
	sent := messageFromUpdates(t, updatesClass)
	if sent.Message != "prepared text" {
		t.Fatalf("sent prepared message = %q, want prepared text", sent.Message)
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("sent prepared via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
}

func TestBotAPISavePreparedInlineMessageUsesRouterRegistry(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)

	id, expireDate, err := f.router.SavePreparedInlineMessageFromBotAPI(ctx, f.bot.ID, f.owner.ID, domain.BotInlineResult{
		ID:      "botapi-prepared",
		Type:    "article",
		Message: "prepared from bot api",
	}, []string{store.InlineQueryPeerTypePM})
	if err != nil {
		t.Fatalf("save prepared inline from bot api: %v", err)
	}
	if id == "" || expireDate == 0 {
		t.Fatalf("prepared id/expire = %q/%d, want non-empty", id, expireDate)
	}
	got, err := f.router.onMessagesGetPreparedInlineMessage(ownerCtx, &tg.MessagesGetPreparedInlineMessageRequest{
		Bot: inputUser(f.bot),
		ID:  id,
	})
	if err != nil {
		t.Fatalf("get bot api prepared inline: %v", err)
	}
	result, ok := got.Result.(*tg.BotInlineResult)
	if !ok || result.ID != "botapi-prepared" {
		t.Fatalf("prepared result = %T/%#v, want botapi-prepared", got.Result, got.Result)
	}
	if len(got.PeerTypes) != 1 {
		t.Fatalf("prepared peer types = %d, want 1", len(got.PeerTypes))
	}
	if _, ok := got.PeerTypes[0].(*tg.InlineQueryPeerTypePM); !ok {
		t.Fatalf("prepared peer type = %T, want PM", got.PeerTypes[0])
	}
	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 9933,
		QueryID:  got.QueryID,
		ID:       result.ID,
	})
	if err != nil {
		t.Fatalf("send bot api prepared inline result: %v", err)
	}
	sent := messageFromUpdates(t, updatesClass)
	if sent.Message != "prepared from bot api" {
		t.Fatalf("sent prepared message = %q, want prepared from bot api", sent.Message)
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("sent prepared via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
}

func TestMessagesPreparedInlinePeerTypesRestrictSend(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	botCtx := WithUserID(ctx, f.bot.ID)
	ownerCtx := WithUserID(ctx, f.owner.ID)

	save := &tg.MessagesSavePreparedInlineMessageRequest{
		Result: preparedInlineArticleResult(),
		UserID: inputUser(f.owner),
	}
	save.SetPeerTypes([]tg.InlineQueryPeerTypeClass{&tg.InlineQueryPeerTypeBroadcast{}})
	prepared, err := f.router.onMessagesSavePreparedInlineMessage(botCtx, save)
	if err != nil {
		t.Fatalf("save prepared inline: %v", err)
	}
	got, err := f.router.onMessagesGetPreparedInlineMessage(ownerCtx, &tg.MessagesGetPreparedInlineMessageRequest{
		Bot: inputUser(f.bot),
		ID:  prepared.ID,
	})
	if err != nil {
		t.Fatalf("get prepared inline: %v", err)
	}
	if _, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 9932,
		QueryID:  got.QueryID,
		ID:       "prepared-article",
	}); !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("send prepared to disallowed peer err = %v, want PEER_ID_INVALID", err)
	}
}

func TestMessagesBotLongtailExplicitStubsAreRegistered(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	userCtx := WithUserID(ctx, f.owner.ID)

	tests := []struct {
		name string
		req  bin.Encoder
		want string
	}{
		{
			name: "sendBotRequestedPeer",
			req: &tg.MessagesSendBotRequestedPeerRequest{
				Peer:           inputPeerUser(f.bot),
				ButtonID:       1,
				RequestedPeers: []tg.InputPeerClass{inputPeerUser(f.peer)},
			},
			want: "BUTTON_DATA_INVALID",
		},
		{
			name: "getPreparedInlineMessage",
			req: &tg.MessagesGetPreparedInlineMessageRequest{
				Bot: inputUser(f.bot),
				ID:  "prepared-1",
			},
			want: "RESULT_ID_INVALID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in bin.Buffer
			if err := tt.req.Encode(&in); err != nil {
				t.Fatalf("encode request: %v", err)
			}
			if _, err := f.router.Dispatch(userCtx, [8]byte{}, 0, &in); !tgerr.Is(err, tt.want) {
				t.Fatalf("dispatch err = %v, want %s", err, tt.want)
			}
		})
	}
}
