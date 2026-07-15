package rpc

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func TestMessagesRequestWebViewProlongAndSendResult(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	req := &tg.MessagesRequestWebViewRequest{
		Peer:     inputPeerUser(f.peer),
		Bot:      inputUser(f.bot),
		Platform: "tdesktop",
		Silent:   true,
	}
	req.SetURL("https://example.test/app?existing=1")
	req.SetStartParam("start_1")
	got, err := f.router.onMessagesRequestWebView(ownerCtx, req)
	if err != nil {
		t.Fatalf("request webview: %v", err)
	}
	queryID, ok := got.GetQueryID()
	if !ok || queryID == 0 {
		t.Fatalf("request webview query_id = %d,%v, want non-zero", queryID, ok)
	}
	initData := webViewInitDataFromURL(t, got.URL)
	botQueryID := initData.Get("query_id")
	if botQueryID != strconv.FormatInt(queryID, 10) {
		t.Fatalf("init query_id = %q, want %d", botQueryID, queryID)
	}
	if initData.Get("hash") == "" || initData.Get("auth_date") == "" || initData.Get("start_param") != "start_1" {
		t.Fatalf("init data missing signed fields: %s", initData.Encode())
	}
	if gotURL, _ := url.Parse(got.URL); gotURL.Query().Get("existing") != "1" {
		t.Fatalf("webview url dropped existing query: %q", got.URL)
	}

	ok, err = f.router.onMessagesProlongWebView(ownerCtx, &tg.MessagesProlongWebViewRequest{
		Peer:    inputPeerUser(f.peer),
		Bot:     inputUser(f.bot),
		QueryID: queryID,
		Silent:  true,
	})
	if err != nil || !ok {
		t.Fatalf("prolong webview = %v,%v, want true,nil", ok, err)
	}

	if _, err := f.router.onMessagesSendWebViewResultMessage(botCtx, &tg.MessagesSendWebViewResultMessageRequest{
		BotQueryID: botQueryID,
		Result:     inlineArticleResult("webview-article", "from webview"),
	}); err != nil {
		t.Fatalf("send webview result: %v", err)
	}
	if f.router.webviews.size() != 0 {
		t.Fatalf("webview registry size = %d, want consumed", f.router.webviews.size())
	}
	historyList, err := f.router.deps.Messages.GetHistory(ownerCtx, f.owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	history := tgMessagesMessages(f.owner.ID, f.router.enrichMessageList(ownerCtx, f.owner.ID, historyList))
	messages := messagesFromClass(history)
	if len(messages) != 1 {
		t.Fatalf("history messages = %d, want 1", len(messages))
	}
	sent, ok := messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("history message = %T, want *tg.Message", messages[0])
	}
	if sent.Message != "from webview" {
		t.Fatalf("history text = %q, want from webview", sent.Message)
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("history via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
}

func TestBotAPIAnswerWebAppQueryUsesRouterWebViewSession(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)

	req := &tg.MessagesRequestWebViewRequest{
		Peer:     inputPeerUser(f.peer),
		Bot:      inputUser(f.bot),
		Platform: "tdesktop",
	}
	req.SetURL("https://example.test/botapi")
	got, err := f.router.onMessagesRequestWebView(ownerCtx, req)
	if err != nil {
		t.Fatalf("request webview: %v", err)
	}
	botQueryID := webViewInitDataFromURL(t, got.URL).Get("query_id")
	if botQueryID == "" {
		t.Fatal("bot query id empty")
	}

	if _, err := f.router.AnswerWebAppQueryFromBotAPI(ctx, f.bot.ID, botQueryID, domain.BotInlineResult{
		ID:      "botapi-webapp",
		Type:    "article",
		Message: "from bot api webapp",
	}); err != nil {
		t.Fatalf("answer web app query from bot api: %v", err)
	}
	if f.router.webviews.size() != 0 {
		t.Fatalf("webview registry size = %d, want consumed", f.router.webviews.size())
	}
	historyList, err := f.router.deps.Messages.GetHistory(ownerCtx, f.owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	history := tgMessagesMessages(f.owner.ID, f.router.enrichMessageList(ownerCtx, f.owner.ID, historyList))
	messages := messagesFromClass(history)
	if len(messages) != 1 {
		t.Fatalf("history messages = %d, want 1", len(messages))
	}
	sent, ok := messages[0].(*tg.Message)
	if !ok || sent.Message != "from bot api webapp" {
		t.Fatalf("history message = %T %+v, want bot api webapp text", messages[0], messages[0])
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("history via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
	if _, err := f.router.AnswerWebAppQueryFromBotAPI(ctx, f.bot.ID, botQueryID, domain.BotInlineResult{
		ID:      "botapi-webapp-repeat",
		Type:    "article",
		Message: "repeat",
	}); err == nil || !strings.Contains(err.Error(), "QUERY_ID_INVALID") {
		t.Fatalf("repeat answer err = %v, want QUERY_ID_INVALID", err)
	}
}

func TestMessagesWebViewRejectsInvalidSessionAndKeepsSessionOnBadResult(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	req := &tg.MessagesRequestWebViewRequest{
		Peer:     inputPeerUser(f.peer),
		Bot:      inputUser(f.bot),
		Platform: "tdesktop",
	}
	req.SetURL("https://example.test/app")
	got, err := f.router.onMessagesRequestWebView(ownerCtx, req)
	if err != nil {
		t.Fatalf("request webview: %v", err)
	}
	queryID, _ := got.GetQueryID()
	if _, err := f.router.onMessagesProlongWebView(ownerCtx, &tg.MessagesProlongWebViewRequest{
		Peer:    inputPeerUser(f.owner),
		Bot:     inputUser(f.bot),
		QueryID: queryID,
	}); err == nil || !strings.Contains(err.Error(), "QUERY_ID_INVALID") {
		t.Fatalf("wrong peer prolong err = %v, want QUERY_ID_INVALID", err)
	}

	botQueryID := webViewInitDataFromURL(t, got.URL).Get("query_id")
	if _, err := f.router.onMessagesSendWebViewResultMessage(ownerCtx, &tg.MessagesSendWebViewResultMessageRequest{
		BotQueryID: botQueryID,
		Result:     inlineArticleResult("webview-article", "from webview"),
	}); err == nil || !strings.Contains(err.Error(), "USER_BOT_REQUIRED") {
		t.Fatalf("non-bot send webview result err = %v, want USER_BOT_REQUIRED", err)
	}
	if _, err := f.router.onMessagesSendWebViewResultMessage(botCtx, &tg.MessagesSendWebViewResultMessageRequest{
		BotQueryID: botQueryID,
		Result: &tg.InputBotInlineResult{
			ID:          "bad",
			Type:        "photo",
			Title:       "bad",
			SendMessage: &tg.InputBotInlineMessageText{Message: "bad"},
		},
	}); err == nil || !strings.Contains(err.Error(), "RESULT_TYPE_INVALID") {
		t.Fatalf("bad result err = %v, want RESULT_TYPE_INVALID", err)
	}
	if f.router.webviews.size() != 1 {
		t.Fatalf("webview registry size after bad result = %d, want 1", f.router.webviews.size())
	}
	if _, err := f.router.onMessagesSendWebViewResultMessage(botCtx, &tg.MessagesSendWebViewResultMessageRequest{
		BotQueryID: botQueryID,
		Result:     inlineArticleResult("webview-article", "ok after bad result"),
	}); err != nil {
		t.Fatalf("send webview result after bad result: %v", err)
	}
}

func TestMessagesRequestWebViewFromMenuUsesConfiguredURL(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	if _, err := f.bots.SetBotMenuButton(ctx, f.bot.ID, domain.BotMenuButton{
		Type: domain.BotMenuButtonWebView,
		Text: "Open",
		URL:  "https://example.test/menu",
	}); err != nil {
		t.Fatalf("set menu button: %v", err)
	}
	got, err := f.router.onMessagesRequestWebView(WithUserID(ctx, f.owner.ID), &tg.MessagesRequestWebViewRequest{
		FromBotMenu: true,
		Peer:        inputPeerUser(f.bot),
		Bot:         inputUser(f.bot),
		Platform:    "tdesktop",
	})
	if err != nil {
		t.Fatalf("request menu webview: %v", err)
	}
	if !strings.HasPrefix(got.URL, "https://example.test/menu?") {
		t.Fatalf("menu webview url = %q, want configured URL", got.URL)
	}
	if _, ok := got.GetQueryID(); !ok {
		t.Fatal("menu webview missing query_id")
	}
}

func TestMessagesGetBotAppAndRequestAppWebViewUsesMenuBackedSession(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	if _, err := f.bots.SetBotMenuButton(ctx, f.bot.ID, domain.BotMenuButton{
		Type: domain.BotMenuButtonWebView,
		Text: "Launch",
		URL:  "https://example.test/direct",
	}); err != nil {
		t.Fatalf("set menu button: %v", err)
	}

	gotApp, err := f.router.onMessagesGetBotApp(WithUserID(ctx, f.owner.ID), &tg.MessagesGetBotAppRequest{
		App: &tg.InputBotAppShortName{
			BotID:     inputUser(f.bot),
			ShortName: "main",
		},
	})
	if err != nil {
		t.Fatalf("get bot app: %v", err)
	}
	app, ok := gotApp.App.(*tg.BotApp)
	if !ok {
		t.Fatalf("bot app = %T, want *tg.BotApp", gotApp.App)
	}
	if app.ID == 0 || app.AccessHash == 0 || app.ShortName != "main" || app.Title != "Launch" {
		t.Fatalf("bot app = %+v, want catalog-backed menu app", app)
	}

	cached, err := f.router.onMessagesGetBotApp(WithUserID(ctx, f.owner.ID), &tg.MessagesGetBotAppRequest{
		App: &tg.InputBotAppShortName{
			BotID:     inputUser(f.bot),
			ShortName: "main",
		},
		Hash: app.Hash,
	})
	if err != nil {
		t.Fatalf("get cached bot app: %v", err)
	}
	if _, ok := cached.App.(*tg.BotAppNotModified); !ok {
		t.Fatalf("cached app = %T, want *tg.BotAppNotModified", cached.App)
	}

	req := &tg.MessagesRequestAppWebViewRequest{
		Peer:       &tg.InputPeerEmpty{},
		App:        &tg.InputBotAppID{ID: app.ID, AccessHash: app.AccessHash},
		Platform:   "tdesktop",
		Fullscreen: true,
	}
	req.SetStartParam("start_app")
	got, err := f.router.onMessagesRequestAppWebView(WithUserID(ctx, f.owner.ID), req)
	if err != nil {
		t.Fatalf("request app webview: %v", err)
	}
	if fullscreen := got.GetFullscreen(); !fullscreen {
		t.Fatal("request app webview fullscreen = false, want true")
	}
	if !strings.HasPrefix(got.URL, "https://example.test/direct?") {
		t.Fatalf("app webview url = %q, want configured URL", got.URL)
	}
	queryID, ok := got.GetQueryID()
	if !ok || queryID == 0 {
		t.Fatalf("app webview query_id = %d,%v, want non-zero", queryID, ok)
	}
	initData := webViewInitDataFromURL(t, got.URL)
	if initData.Get("start_param") != "start_app" {
		t.Fatalf("start_param = %q, want start_app", initData.Get("start_param"))
	}

	if _, err := f.router.onMessagesSendWebViewResultMessage(WithUserID(ctx, f.bot.ID), &tg.MessagesSendWebViewResultMessageRequest{
		BotQueryID: initData.Get("query_id"),
		Result:     inlineArticleResult("app-webview", "from app"),
	}); err != nil {
		t.Fatalf("send app webview result: %v", err)
	}
	historyList, err := f.router.deps.Messages.GetHistory(ctx, f.owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get bot peer history: %v", err)
	}
	history := tgMessagesMessages(f.owner.ID, f.router.enrichMessageList(ctx, f.owner.ID, historyList))
	messages := messagesFromClass(history)
	if len(messages) != 1 {
		t.Fatalf("bot peer history messages = %d, want 1", len(messages))
	}
	sent, ok := messages[0].(*tg.Message)
	if !ok || sent.Message != "from app" {
		t.Fatalf("bot peer message = %T %+v, want text from app", messages[0], messages[0])
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("app message via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
}

func TestMessagesRequestMainWebViewUsesMenuBackedSession(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	if _, err := f.bots.SetBotMenuButton(ctx, f.bot.ID, domain.BotMenuButton{
		Type: domain.BotMenuButtonWebView,
		Text: "Main",
		URL:  "https://example.test/main",
	}); err != nil {
		t.Fatalf("set menu button: %v", err)
	}

	req := &tg.MessagesRequestMainWebViewRequest{
		Peer:     &tg.InputPeerEmpty{},
		Bot:      inputUser(f.bot),
		Platform: "android",
	}
	req.SetStartParam("from_profile")
	got, err := f.router.onMessagesRequestMainWebView(WithUserID(ctx, f.owner.ID), req)
	if err != nil {
		t.Fatalf("request main webview: %v", err)
	}
	if !strings.HasPrefix(got.URL, "https://example.test/main?") {
		t.Fatalf("main webview url = %q, want configured URL", got.URL)
	}
	queryID, ok := got.GetQueryID()
	if !ok || queryID == 0 {
		t.Fatalf("main webview query_id = %d,%v, want non-zero", queryID, ok)
	}
	initData := webViewInitDataFromURL(t, got.URL)
	if initData.Get("start_param") != "from_profile" {
		t.Fatalf("start_param = %q, want from_profile", initData.Get("start_param"))
	}
	if _, err := f.router.onMessagesProlongWebView(WithUserID(ctx, f.owner.ID), &tg.MessagesProlongWebViewRequest{
		Peer:    inputPeerUser(f.bot),
		Bot:     inputUser(f.bot),
		QueryID: queryID,
	}); err != nil {
		t.Fatalf("prolong main webview using bot peer fallback: %v", err)
	}
}

func TestMessagesBotAppRejectsInvalidInputs(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	userCtx := WithUserID(ctx, f.owner.ID)

	if _, err := f.router.onMessagesGetBotApp(userCtx, &tg.MessagesGetBotAppRequest{
		App: &tg.InputBotAppShortName{BotID: inputUser(f.bot), ShortName: "main"},
	}); err == nil || !strings.Contains(err.Error(), "BOT_APP_INVALID") {
		t.Fatalf("get app without menu err = %v, want BOT_APP_INVALID", err)
	}
	if _, err := f.bots.SetBotMenuButton(ctx, f.bot.ID, domain.BotMenuButton{
		Type: domain.BotMenuButtonWebView,
		Text: "Launch",
		URL:  "https://example.test/app",
	}); err != nil {
		t.Fatalf("set menu button: %v", err)
	}
	if _, err := f.router.onMessagesGetBotApp(userCtx, &tg.MessagesGetBotAppRequest{
		App: &tg.InputBotAppShortName{BotID: inputUser(f.bot), ShortName: "bad-name"},
	}); err == nil || !strings.Contains(err.Error(), "BOT_APP_SHORTNAME_INVALID") {
		t.Fatalf("bad short name err = %v, want BOT_APP_SHORTNAME_INVALID", err)
	}
	if _, err := f.router.onMessagesGetBotApp(userCtx, &tg.MessagesGetBotAppRequest{
		App: &tg.InputBotAppShortName{BotID: inputUser(f.owner), ShortName: "main"},
	}); err == nil || !strings.Contains(err.Error(), "BOT_APP_BOT_INVALID") {
		t.Fatalf("non-bot owner err = %v, want BOT_APP_BOT_INVALID", err)
	}
	if _, err := f.router.onMessagesRequestAppWebView(userCtx, &tg.MessagesRequestAppWebViewRequest{
		Peer:     inputPeerUser(f.bot),
		App:      &tg.InputBotAppID{ID: f.bot.ID, AccessHash: 123},
		Platform: "tdesktop",
	}); err == nil || !strings.Contains(err.Error(), "BOT_APP_INVALID") {
		t.Fatalf("wrong app access hash err = %v, want BOT_APP_INVALID", err)
	}
	if _, err := f.router.onMessagesRequestMainWebView(userCtx, &tg.MessagesRequestMainWebViewRequest{
		Peer:     &tg.InputPeerEmpty{},
		Bot:      inputUser(f.owner),
		Platform: "tdesktop",
	}); err == nil || !strings.Contains(err.Error(), "BOT_INVALID") {
		t.Fatalf("main non-bot err = %v, want BOT_INVALID", err)
	}
}

func webViewInitDataFromURL(t *testing.T, raw string) url.Values {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse webview url: %v", err)
	}
	initData := parsed.Query().Get("tgWebAppData")
	if initData == "" {
		t.Fatalf("webview url missing tgWebAppData: %q", raw)
	}
	values, err := url.ParseQuery(initData)
	if err != nil {
		t.Fatalf("parse init data: %v", err)
	}
	return values
}
