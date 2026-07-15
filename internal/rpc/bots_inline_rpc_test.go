package rpc

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	botsapp "telesrv/internal/app/bots"
	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type inlineBotRPCTestFixture struct {
	router   *Router
	bots     *botsapp.Service
	channels *appchannels.Service
	files    *fakeFiles
	owner    domain.User
	peer     domain.User
	bot      domain.User
	photo    domain.Photo
	document domain.Document
}

func newInlineBotRPCTestFixture(t *testing.T) inlineBotRPCTestFixture {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	botStore := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogs)
	bots := botsapp.NewService(users, botStore, messageStore)
	messages := appmessages.NewService(messageStore, dialogs)
	channelStore := memory.NewChannelStore()
	channels := appchannels.NewService(channelStore, appchannels.WithBotProfileResolver(bots))
	files := &fakeFiles{
		docs:   map[int64]domain.Document{},
		photos: map[int64]domain.Photo{},
	}
	photo := files.putPhoto(domain.Photo{
		ID:         8101,
		AccessHash: 810101,
		DCID:       2,
		Sizes:      []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 640, H: 480, Size: 1234}},
	})
	document := domain.Document{
		ID:         8201,
		AccessHash: 820101,
		DCID:       2,
		MimeType:   "application/pdf",
		Size:       4567,
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "inline.pdf"}},
		Thumbs:     []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "m", W: 160, H: 120, Size: 321}},
	}
	files.docs[document.ID] = document
	owner, err := users.Create(ctx, domain.User{AccessHash: 7101, Phone: "15550007101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	peer, err := users.Create(ctx, domain.User{AccessHash: 7102, Phone: "15550007102", FirstName: "Peer"})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}
	bot, _, err := bots.CreateBot(ctx, owner.ID, "Inline Bot", "inline_shape_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	if _, err := bots.SetInlinePlaceholder(ctx, bot.ID, "Search inline"); err != nil {
		t.Fatalf("set inline placeholder: %v", err)
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(users),
		Bots:     bots,
		Messages: messages,
		Channels: channels,
		Files:    files,
	}, zaptest.NewLogger(t), clock.System)
	return inlineBotRPCTestFixture{router: r, bots: bots, channels: channels, files: files, owner: owner, peer: peer, bot: bot, photo: photo, document: document}
}

func TestInlineBotArticleTextPrivateRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, &tg.MessagesGetInlineBotResultsRequest{
			Bot:   inputUser(f.bot),
			Peer:  inputPeerUser(f.peer),
			Query: "hello",
		})
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			&tg.InputBotInlineResult{
				ID:          "article-1",
				Type:        "article",
				Title:       "Echo",
				Description: "Echo text",
				SendMessage: &tg.InputBotInlineMessageText{Message: "inline hello"},
			},
		},
	}); err != nil || !ok {
		t.Fatalf("set inline results = %v,%v, want true,nil", ok, err)
	}

	var got getResult
	select {
	case got = <-gotCh:
	case <-time.After(2 * time.Second):
		t.Fatal("get inline results did not resolve")
	}
	if got.err != nil {
		t.Fatalf("get inline results: %v", got.err)
	}
	if got.res.QueryID != queryID || len(got.res.Results) != 1 {
		t.Fatalf("inline results = query %d len %d, want query %d len 1", got.res.QueryID, len(got.res.Results), queryID)
	}
	if len(got.res.Users) != 1 {
		t.Fatalf("inline result users = %d, want bot user", len(got.res.Users))
	}
	botUser, ok := got.res.Users[0].(*tg.User)
	if !ok || botUser.ID != f.bot.ID || !botUser.Bot {
		t.Fatalf("inline result user = %#v, want bot user %d", got.res.Users[0], f.bot.ID)
	}
	if placeholder, ok := botUser.GetBotInlinePlaceholder(); !ok || placeholder != "Search inline" {
		t.Fatalf("bot inline placeholder = %q,%v, want Search inline,true", placeholder, ok)
	}

	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 9901,
		QueryID:  queryID,
		ID:       "article-1",
	})
	if err != nil {
		t.Fatalf("send inline bot result: %v", err)
	}
	sent := messageFromUpdates(t, updatesClass)
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("sent via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
	if sent.Message != "inline hello" {
		t.Fatalf("sent message = %q, want inline hello", sent.Message)
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
	historyMessages, ok := history.(*tg.MessagesMessages)
	if !ok || len(historyMessages.Messages) != 1 {
		t.Fatalf("history = %T len %d, want messagesMessages len 1", history, len(messagesFromClass(history)))
	}
	histMsg, ok := historyMessages.Messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("history message = %T, want *tg.Message", historyMessages.Messages[0])
	}
	if via, ok := histMsg.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("history via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
	foundBot := false
	for _, u := range historyMessages.Users {
		if user, ok := u.(*tg.User); ok && user.ID == f.bot.ID && user.Bot {
			foundBot = true
		}
	}
	if !foundBot {
		t.Fatalf("history users missing via bot: %+v", historyMessages.Users)
	}
}

func TestInlineBotSendKeepsQueryPeerBinding(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	queryID, _ := f.router.inlines.register(f.router.clock.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID})
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID: queryID,
		Results: []tg.InputBotInlineResultClass{
			inlineArticleResult("article-locked", "locked"),
		},
	}); err != nil || !ok {
		t.Fatalf("set inline results = %v,%v, want true,nil", ok, err)
	}
	if _, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.owner),
		RandomID: 9902,
		QueryID:  queryID,
		ID:       "article-locked",
	}); !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("send inline to different peer err = %v, want PEER_ID_INVALID", err)
	}
}

func TestInlineBotArticleTextExternalThumbRoundTripAndCache(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	req := &tg.MessagesGetInlineBotResultsRequest{
		Bot:   inputUser(f.bot),
		Peer:  inputPeerUser(f.peer),
		Query: "preview",
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, req)
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			inlineArticleResultWithWebThumb("article-web", "preview text"),
		},
	}); err != nil || !ok {
		t.Fatalf("set inline results = %v,%v, want true,nil", ok, err)
	}

	var got getResult
	select {
	case got = <-gotCh:
	case <-time.After(2 * time.Second):
		t.Fatal("get inline results did not resolve")
	}
	if got.err != nil {
		t.Fatalf("get inline results: %v", got.err)
	}
	assertInlineArticleWebPreview(t, got.res, queryID)

	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 9911,
		QueryID:  queryID,
		ID:       "article-web",
	})
	if err != nil {
		t.Fatalf("send inline bot result: %v", err)
	}
	sent := messageFromUpdates(t, updatesClass)
	if sent.Message != "preview text" {
		t.Fatalf("sent message = %q, want preview text", sent.Message)
	}
	if sent.Media != nil {
		t.Fatalf("sent media = %T, want nil for text article", sent.Media)
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("sent via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}

	cached, err := f.router.onMessagesGetInlineBotResults(ownerCtx, req)
	if err != nil {
		t.Fatalf("cached get inline results: %v", err)
	}
	if cached.QueryID == queryID {
		t.Fatalf("cached query_id reused %d, want fresh id", cached.QueryID)
	}
	assertInlineArticleWebPreview(t, cached, cached.QueryID)
	if f.router.inlines.unansweredSize() != 0 {
		t.Fatalf("cache hit left unanswered pending queries = %d, want 0", f.router.inlines.unansweredSize())
	}
}

func TestInlineBotArticleTextExternalContentWebFileDownload(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	oldFetch := fetchInlineWebFile
	fetches := 0
	fetchInlineWebFile = func(_ context.Context, document domain.BotInlineWebDocument) ([]byte, string, error) {
		fetches++
		if document.URL != "https://example.test/content.png" || document.AccessHash == 0 {
			t.Fatalf("fetch document = %+v", document)
		}
		return []byte("abcdef"), "image/png; charset=binary", nil
	}
	defer func() { fetchInlineWebFile = oldFetch }()

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	req := &tg.MessagesGetInlineBotResultsRequest{
		Bot:   inputUser(f.bot),
		Peer:  inputPeerUser(f.peer),
		Query: "content",
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, req)
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			inlineArticleResultWithWebContent("article-content", "content text"),
		},
	}); err != nil || !ok {
		t.Fatalf("set inline results = %v,%v, want true,nil", ok, err)
	}

	var got getResult
	select {
	case got = <-gotCh:
	case <-time.After(2 * time.Second):
		t.Fatal("get inline results did not resolve")
	}
	if got.err != nil {
		t.Fatalf("get inline results: %v", got.err)
	}
	result, ok := got.res.Results[0].(*tg.BotInlineResult)
	if !ok {
		t.Fatalf("inline result = %T, want *tg.BotInlineResult", got.res.Results[0])
	}
	rawContent, ok := result.GetContent()
	if !ok {
		t.Fatal("inline result missing content")
	}
	content, ok := rawContent.(*tg.WebDocument)
	if !ok {
		t.Fatalf("inline content = %T, want *tg.WebDocument", rawContent)
	}
	if content.URL != "https://example.test/content.png" || content.AccessHash == 0 || content.MimeType != "image/png" {
		t.Fatalf("content = %+v", content)
	}
	file, err := f.router.onUploadGetWebFile(ownerCtx, &tg.UploadGetWebFileRequest{
		Location: &tg.InputWebFileLocation{URL: content.URL, AccessHash: content.AccessHash},
		Offset:   2,
		Limit:    3,
	})
	if err != nil {
		t.Fatalf("upload.getWebFile content: %v", err)
	}
	if string(file.Bytes) != "cde" || file.Size != 6 || file.MimeType != "image/png" {
		t.Fatalf("webfile = size %d mime %q bytes %q", file.Size, file.MimeType, file.Bytes)
	}
	file, err = f.router.onUploadGetWebFile(ownerCtx, &tg.UploadGetWebFileRequest{
		Location: &tg.InputWebFileLocation{URL: content.URL, AccessHash: content.AccessHash},
		Offset:   0,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("upload.getWebFile cached content: %v", err)
	}
	if string(file.Bytes) != "ab" || fetches != 1 {
		t.Fatalf("cached webfile bytes=%q fetches=%d, want ab and one fetch", file.Bytes, fetches)
	}
	if _, err := f.router.onUploadGetWebFile(ownerCtx, &tg.UploadGetWebFileRequest{
		Location: &tg.InputWebFileLocation{URL: content.URL, AccessHash: content.AccessHash + 1},
		Offset:   0,
		Limit:    2,
	}); err == nil || !strings.Contains(err.Error(), "LOCATION_INVALID") {
		t.Fatalf("bad access_hash err = %v, want LOCATION_INVALID", err)
	}
	if _, err := f.router.onUploadGetWebFile(ownerCtx, &tg.UploadGetWebFileRequest{
		Location: &tg.InputWebFileLocation{URL: "https://example.test/other.png", AccessHash: content.AccessHash},
		Offset:   0,
		Limit:    2,
	}); err == nil || !strings.Contains(err.Error(), "LOCATION_INVALID") {
		t.Fatalf("bad url err = %v, want LOCATION_INVALID", err)
	}
}

func TestInlineBotExternalPhotoContentMediaAutoRoundTripAndCache(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	oldFetch := fetchInlineWebFile
	fetches := 0
	body := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 1, 2, 3, 4}
	fetchInlineWebFile = func(_ context.Context, document domain.BotInlineWebDocument) ([]byte, string, error) {
		fetches++
		if document.URL != "https://example.test/photo.png" || document.AccessHash == 0 {
			t.Fatalf("fetch document = %+v", document)
		}
		return append([]byte(nil), body...), "image/png; charset=binary", nil
	}
	defer func() { fetchInlineWebFile = oldFetch }()

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	req := &tg.MessagesGetInlineBotResultsRequest{
		Bot:   inputUser(f.bot),
		Peer:  inputPeerUser(f.peer),
		Query: "external-photo",
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, req)
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			inlineExternalPhotoContentResult("external-photo", "external photo caption"),
		},
	}); err != nil || !ok {
		t.Fatalf("set external photo result = %v,%v, want true,nil", ok, err)
	}

	var got getResult
	select {
	case got = <-gotCh:
	case <-time.After(2 * time.Second):
		t.Fatal("external photo get inline results did not resolve")
	}
	if got.err != nil {
		t.Fatalf("get external photo results: %v", got.err)
	}
	result, ok := got.res.Results[0].(*tg.BotInlineResult)
	if !ok {
		t.Fatalf("external photo result = %T, want *tg.BotInlineResult", got.res.Results[0])
	}
	if _, ok := result.SendMessage.(*tg.BotInlineMessageMediaAuto); !ok {
		t.Fatalf("external photo send_message = %T, want mediaAuto", result.SendMessage)
	}
	if content, ok := result.GetContent(); !ok {
		t.Fatal("external photo result missing content")
	} else if doc, ok := content.(*tg.WebDocument); !ok || doc.URL != "https://example.test/photo.png" || doc.AccessHash == 0 {
		t.Fatalf("external photo content = %#v", content)
	}

	send := func(queryID int64, randomID int64) *tg.Message {
		t.Helper()
		updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
			Peer:     inputPeerUser(f.peer),
			RandomID: randomID,
			QueryID:  queryID,
			ID:       "external-photo",
		})
		if err != nil {
			t.Fatalf("send external photo result: %v", err)
		}
		sent := messageFromUpdates(t, updatesClass)
		if sent.Message != "external photo caption" {
			t.Fatalf("sent external photo caption = %q", sent.Message)
		}
		if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
			t.Fatalf("sent external photo via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
		}
		if media, ok := sent.Media.(*tg.MessageMediaPhoto); !ok {
			t.Fatalf("sent external media = %T, want *tg.MessageMediaPhoto", sent.Media)
		} else if photo, ok := media.Photo.(*tg.Photo); !ok || photo.ID == 0 || photo.ID == f.photo.ID {
			t.Fatalf("sent external photo = %#v, want generated photo", media.Photo)
		}
		return sent
	}
	send(queryID, 9921)

	cached, err := f.router.onMessagesGetInlineBotResults(ownerCtx, req)
	if err != nil {
		t.Fatalf("cached external photo get: %v", err)
	}
	if cached.QueryID == queryID {
		t.Fatalf("cached external photo query_id reused %d", queryID)
	}
	send(cached.QueryID, 9922)
	if fetches != 1 {
		t.Fatalf("external photo fetches = %d, want one fetch reused by cached send", fetches)
	}

	historyList, err := f.router.deps.Messages.GetHistory(ownerCtx, f.owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("external photo history: %v", err)
	}
	history := tgMessagesMessages(f.owner.ID, f.router.enrichMessageList(ownerCtx, f.owner.ID, historyList))
	messages := messagesFromClass(history)
	if len(messages) < 2 {
		t.Fatalf("external photo history len = %d, want at least two messages", len(messages))
	}
	histMsg, ok := messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("external photo history message = %T, want *tg.Message", messages[0])
	}
	if _, ok := histMsg.Media.(*tg.MessageMediaPhoto); !ok {
		t.Fatalf("external photo history media = %T, want *tg.MessageMediaPhoto", histMsg.Media)
	}
	if via, ok := histMsg.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("external photo history via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
}

func TestInlineBotExternalContentSendFetchFailureDoesNotSend(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	oldFetch := fetchInlineWebFile
	fetchInlineWebFile = func(context.Context, domain.BotInlineWebDocument) ([]byte, string, error) {
		return nil, "", errors.New("fetch failed")
	}
	defer func() { fetchInlineWebFile = oldFetch }()

	queryID, _ := f.router.inlines.register(f.router.clock.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID})
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID: queryID,
		Results: []tg.InputBotInlineResultClass{
			inlineExternalPhotoContentResult("external-photo", "caption"),
		},
	}); err != nil || !ok {
		t.Fatalf("set external photo result = %v,%v, want true,nil", ok, err)
	}

	if _, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 9923,
		QueryID:  queryID,
		ID:       "external-photo",
	}); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
		t.Fatalf("send failed external photo err = %v, want MEDIA_INVALID", err)
	}
	historyList, err := f.router.deps.Messages.GetHistory(ownerCtx, f.owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("failed external photo history: %v", err)
	}
	if len(historyList.Messages) != 0 {
		t.Fatalf("failed external photo sent %d messages, want none", len(historyList.Messages))
	}
}

func TestInlineBotArticleTextExternalThumbValidation(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	queryID, _ := f.router.inlines.register(f.router.clock.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID})
	defer f.router.inlines.consume(queryID)
	botCtx := WithUserID(ctx, f.bot.ID)

	validThumb := func() tg.InputWebDocument {
		return tg.InputWebDocument{
			URL:      "https://example.test/thumb.jpg",
			Size:     1234,
			MimeType: "image/jpeg",
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeImageSize{W: 96, H: 96},
			},
		}
	}
	cases := []struct {
		name   string
		result tg.InputBotInlineResultClass
		want   string
	}{
		{
			name: "empty thumb url",
			result: func() tg.InputBotInlineResultClass {
				result := inlineArticleResultWithWebThumb("bad-empty-url", "text")
				thumb := validThumb()
				thumb.URL = ""
				result.SetThumb(thumb)
				return result
			}(),
			want: "WEBDOCUMENT_URL_EMPTY",
		},
		{
			name: "http thumb url",
			result: func() tg.InputBotInlineResultClass {
				result := inlineArticleResultWithWebThumb("bad-http-url", "text")
				thumb := validThumb()
				thumb.URL = "http://example.test/thumb.jpg"
				result.SetThumb(thumb)
				return result
			}(),
			want: "WEBDOCUMENT_URL_INVALID",
		},
		{
			name: "bad mime",
			result: func() tg.InputBotInlineResultClass {
				result := inlineArticleResultWithWebThumb("bad-mime", "text")
				thumb := validThumb()
				thumb.MimeType = "image"
				result.SetThumb(thumb)
				return result
			}(),
			want: "WEBDOCUMENT_MIME_INVALID",
		},
		{
			name: "too big",
			result: func() tg.InputBotInlineResultClass {
				result := inlineArticleResultWithWebThumb("too-big", "text")
				thumb := validThumb()
				thumb.Size = domain.MaxBotInlineWebSize + 1
				result.SetThumb(thumb)
				return result
			}(),
			want: "WEBDOCUMENT_SIZE_TOO_BIG",
		},
		{
			name: "content too big",
			result: func() tg.InputBotInlineResultClass {
				result := inlineArticleResultWithWebThumb("content-too-big", "text")
				result.SetContent(tg.InputWebDocument{
					URL:      "https://example.test/content.png",
					Size:     domain.MaxBotInlineWebSize + 1,
					MimeType: "image/png",
				})
				return result
			}(),
			want: "WEBDOCUMENT_SIZE_TOO_BIG",
		},
		{
			name: "content bad mime",
			result: func() tg.InputBotInlineResultClass {
				result := inlineArticleResultWithWebThumb("content-bad-mime", "text")
				result.SetContent(tg.InputWebDocument{
					URL:      "https://example.test/content.png",
					Size:     512,
					MimeType: "image",
				})
				return result
			}(),
			want: "WEBDOCUMENT_MIME_INVALID",
		},
		{
			name: "media auto content mime mismatch",
			result: func() tg.InputBotInlineResultClass {
				result := inlineArticleResultWithWebThumb("content", "text")
				result.Type = "photo"
				result.SendMessage = &tg.InputBotInlineMessageMediaAuto{}
				result.SetContent(tg.InputWebDocument{
					URL:      "https://example.test/content.html",
					Size:     512,
					MimeType: "text/html",
				})
				return result
			}(),
			want: "MEDIA_INVALID",
		},
		{
			name: "geo thumb unsupported",
			result: func() tg.InputBotInlineResultClass {
				result := inlineGeoResult("geo-thumb", 39.9, 116.3).(*tg.InputBotInlineResult)
				result.SetThumb(validThumb())
				return result
			}(),
			want: "WEBDOCUMENT_INVALID",
		},
		{
			name: "result url invalid",
			result: func() tg.InputBotInlineResultClass {
				result := inlineArticleResultWithWebThumb("bad-result-url", "text")
				result.SetURL("ftp://example.test/result")
				return result
			}(),
			want: "WEBDOCUMENT_URL_INVALID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
				QueryID: queryID,
				Results: []tg.InputBotInlineResultClass{
					tc.result,
				},
			}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("set inline results err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestInlineBotArticleTextChannelRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	sessions := &captureSessions{}
	f.router.deps.Sessions = sessions
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	created, err := f.channels.CreateMegagroupFromCreateChat(ctx, f.owner.ID, domain.CreateChannelRequest{
		Title:         "Inline Group",
		MemberUserIDs: []int64{f.peer.ID},
		Date:          1700001000,
	})
	if err != nil {
		t.Fatalf("create inline group: %v", err)
	}
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, &tg.MessagesGetInlineBotResultsRequest{
			Bot:   inputUser(f.bot),
			Peer:  peer,
			Query: "group",
		})
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			inlineArticleResultWithCallback("group-article", "inline group hello", "Open", []byte{0x00, 0xff, 0x42}),
		},
	}); err != nil || !ok {
		t.Fatalf("set channel inline results = %v,%v, want true,nil", ok, err)
	}

	select {
	case got := <-gotCh:
		if got.err != nil {
			t.Fatalf("get channel inline results: %v", got.err)
		}
		if got.res.QueryID != queryID || len(got.res.Results) != 1 {
			t.Fatalf("channel inline results = query %d len %d, want query %d len 1", got.res.QueryID, len(got.res.Results), queryID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel get inline results did not resolve")
	}

	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     peer,
		RandomID: 9902,
		QueryID:  queryID,
		ID:       "group-article",
	})
	if err != nil {
		t.Fatalf("send channel inline bot result: %v", err)
	}
	sent, full := channelMessageFromUpdates(t, updatesClass)
	if sent.Message != "inline group hello" {
		t.Fatalf("channel inline message = %q, want inline group hello", sent.Message)
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("channel send via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
	assertTGInlineReplyMarkup(t, sent, "Open", []byte{0x00, 0xff, 0x42})
	assertUpdatesContainBotUser(t, full, f.bot.ID)

	historyList, err := f.router.deps.Channels.GetHistory(ownerCtx, f.owner.ID, domain.ChannelHistoryFilter{
		ChannelID: created.Channel.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel history: %v", err)
	}
	history := tgChannelHistoryMessages(f.owner.ID, f.router.enrichChannelHistory(ownerCtx, f.owner.ID, historyList))
	channelHistory, ok := history.(*tg.MessagesChannelMessages)
	if !ok || len(channelHistory.Messages) == 0 {
		t.Fatalf("channel history = %T len %d, want channel messages", history, len(channelMessagesFromClass(history)))
	}
	histMsg, ok := channelHistory.Messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("channel history message = %T, want *tg.Message", channelHistory.Messages[0])
	}
	if via, ok := histMsg.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("channel history via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
	assertTGInlineReplyMarkup(t, histMsg, "Open", []byte{0x00, 0xff, 0x42})
	assertChannelMessagesContainBotUser(t, channelHistory, f.bot.ID)

	diff, err := f.router.deps.Channels.GetDifference(ownerCtx, f.owner.ID, domain.ChannelDifferenceRequest{
		UserID:    f.owner.ID,
		ChannelID: created.Channel.ID,
		Pts:       created.Channel.Pts,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel difference: %v", err)
	}
	if len(diff.NewMessages) != 1 {
		t.Fatalf("channel difference messages = %+v, want one new message", diff.NewMessages)
	}
	assertDomainInlineReplyMarkup(t, diff.NewMessages[0].ReplyMarkup, "Open", []byte{0x00, 0xff, 0x42})

	feedback := inlineSendFeedbackFromSessions(t, sessions, f.bot.ID)
	msgID, ok := feedback.GetMsgID()
	if !ok {
		t.Fatal("channel inline send feedback missing msg_id")
	}
	msgID64, ok := msgID.(*tg.InputBotInlineMessageID64)
	if !ok || msgID64.OwnerID != created.Channel.ID || msgID64.ID != sent.ID {
		t.Fatalf("channel inline msg_id = %#v, want id64 owner channel %d msg %d", msgID, created.Channel.ID, sent.ID)
	}
	badID := *msgID64
	badID.AccessHash++
	badEdit := &tg.MessagesEditInlineBotMessageRequest{ID: &badID}
	badEdit.SetMessage("bad edit")
	if ok, err := f.router.onMessagesEditInlineBotMessage(botCtx, badEdit); ok || !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("bad channel inline edit = %v,%v, want false,MESSAGE_ID_INVALID", ok, err)
	}
	editReq := &tg.MessagesEditInlineBotMessageRequest{ID: msgID}
	editReq.SetMessage("inline group edited")
	editReq.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{
		Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonCallback{Text: "Done", Data: []byte("v2")}},
	}}})
	if ok, err := f.router.onMessagesEditInlineBotMessage(botCtx, editReq); err != nil || !ok {
		t.Fatalf("channel inline edit = %v,%v, want true,nil", ok, err)
	}
	editedHistory, err := f.router.deps.Channels.GetHistory(ownerCtx, f.owner.ID, domain.ChannelHistoryFilter{
		ChannelID: created.Channel.ID,
		Limit:     1,
	})
	if err != nil || len(editedHistory.Messages) != 1 {
		t.Fatalf("edited channel history len = %d err=%v, want 1,nil", len(editedHistory.Messages), err)
	}
	if editedHistory.Messages[0].Body != "inline group edited" || editedHistory.Messages[0].ViaBotID != f.bot.ID {
		t.Fatalf("edited channel message = %+v, want edited via bot", editedHistory.Messages[0])
	}
	assertDomainInlineReplyMarkup(t, editedHistory.Messages[0].ReplyMarkup, "Done", []byte("v2"))
	editDiff, err := f.router.deps.Channels.GetDifference(ownerCtx, f.owner.ID, domain.ChannelDifferenceRequest{
		UserID:    f.owner.ID,
		ChannelID: created.Channel.ID,
		Pts:       diff.Pts,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel edit difference: %v", err)
	}
	if len(editDiff.OtherUpdates) != 1 || editDiff.OtherUpdates[0].Type != domain.ChannelUpdateEditMessage {
		t.Fatalf("channel edit difference updates = %+v, want one edit", editDiff.OtherUpdates)
	}
	assertDomainInlineReplyMarkup(t, editDiff.OtherUpdates[0].Message.ReplyMarkup, "Done", []byte("v2"))
}

func TestInlineBotChannelInlineEditRewritesMedia(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	sessions := &captureSessions{}
	f.router.deps.Sessions = sessions
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	created, err := f.channels.CreateMegagroupFromCreateChat(ctx, f.owner.ID, domain.CreateChannelRequest{
		Title:         "Inline Media Edit Group",
		MemberUserIDs: []int64{f.peer.ID},
		Date:          1700002300,
	})
	if err != nil {
		t.Fatalf("create inline media edit group: %v", err)
	}
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	queryID, _ := f.router.inlines.registerWithCacheKey(f.router.clock.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}, inlineCacheKey{query: "channel-media-edit"})
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID: queryID,
		Results: []tg.InputBotInlineResultClass{
			inlineArticleResultWithCallback("channel-media-edit-1", "channel caption before media edit", "Open", []byte("channel-media-v1")),
		},
	}); err != nil || !ok {
		t.Fatalf("set channel media edit inline result = %v,%v, want true,nil", ok, err)
	}
	if _, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     peer,
		RandomID: 9907,
		QueryID:  queryID,
		ID:       "channel-media-edit-1",
	}); err != nil {
		t.Fatalf("send channel media edit inline result: %v", err)
	}
	msgID, ok := inlineSendFeedbackFromSessions(t, sessions, f.bot.ID).GetMsgID()
	if !ok {
		t.Fatal("channel inline media edit feedback missing msg_id")
	}

	editReq := &tg.MessagesEditInlineBotMessageRequest{ID: msgID}
	editReq.SetMedia(&tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPoint{Lat: 40.7128, Long: -74.0060}})
	if ok, err := f.router.onMessagesEditInlineBotMessage(botCtx, editReq); err != nil || !ok {
		t.Fatalf("channel inline media edit = %v,%v, want true,nil", ok, err)
	}

	history, err := f.router.deps.Channels.GetHistory(ownerCtx, f.owner.ID, domain.ChannelHistoryFilter{
		ChannelID: created.Channel.ID,
		Limit:     1,
	})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("channel media edit history len = %d err=%v, want 1,nil", len(history.Messages), err)
	}
	msg := history.Messages[0]
	if msg.Body != "channel caption before media edit" || msg.ViaBotID != f.bot.ID {
		t.Fatalf("channel media edited message = %+v, want original caption via bot", msg)
	}
	if msg.Media == nil || msg.Media.Kind != domain.MessageMediaKindGeo || msg.Media.Geo == nil {
		t.Fatalf("channel media edited media = %+v, want geo", msg.Media)
	}
	assertDomainInlineReplyMarkup(t, msg.ReplyMarkup, "Open", []byte("channel-media-v1"))

	diff, err := f.router.deps.Channels.GetDifference(ownerCtx, f.owner.ID, domain.ChannelDifferenceRequest{
		UserID:    f.owner.ID,
		ChannelID: created.Channel.ID,
		Pts:       created.Channel.Pts + 1,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel media edit difference: %v", err)
	}
	if len(diff.OtherUpdates) != 1 || diff.OtherUpdates[0].Type != domain.ChannelUpdateEditMessage {
		t.Fatalf("channel media edit difference updates = %+v, want one edit", diff.OtherUpdates)
	}
	if diff.OtherUpdates[0].Message.Media == nil || diff.OtherUpdates[0].Message.Media.Kind != domain.MessageMediaKindGeo {
		t.Fatalf("channel media edit difference message = %+v, want geo media", diff.OtherUpdates[0].Message)
	}
	assertDomainInlineReplyMarkup(t, diff.OtherUpdates[0].Message.ReplyMarkup, "Open", []byte("channel-media-v1"))
}

func TestInlineBotPhotoMediaPrivateRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, &tg.MessagesGetInlineBotResultsRequest{
			Bot:   inputUser(f.bot),
			Peer:  inputPeerUser(f.peer),
			Query: "photo",
		})
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			inlinePhotoResult("photo-1", f.photo, "inline photo caption"),
		},
	}); err != nil || !ok {
		t.Fatalf("set inline photo result = %v,%v, want true,nil", ok, err)
	}

	var got getResult
	select {
	case got = <-gotCh:
	case <-time.After(2 * time.Second):
		t.Fatal("photo get inline results did not resolve")
	}
	if got.err != nil {
		t.Fatalf("get inline photo results: %v", got.err)
	}
	mediaResult, ok := got.res.Results[0].(*tg.BotInlineMediaResult)
	if !ok {
		t.Fatalf("inline photo result = %T, want *tg.BotInlineMediaResult", got.res.Results[0])
	}
	if photo, ok := mediaResult.Photo.(*tg.Photo); !ok || photo.ID != f.photo.ID {
		t.Fatalf("inline result photo = %#v, want photo %d", mediaResult.Photo, f.photo.ID)
	}

	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 9903,
		QueryID:  queryID,
		ID:       "photo-1",
	})
	if err != nil {
		t.Fatalf("send inline photo result: %v", err)
	}
	sent := messageFromUpdates(t, updatesClass)
	if sent.Message != "inline photo caption" {
		t.Fatalf("sent photo caption = %q, want inline photo caption", sent.Message)
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("sent photo via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
	photoMedia, ok := sent.Media.(*tg.MessageMediaPhoto)
	if !ok {
		t.Fatalf("sent media = %T, want *tg.MessageMediaPhoto", sent.Media)
	}
	if photo, ok := photoMedia.Photo.(*tg.Photo); !ok || photo.ID != f.photo.ID {
		t.Fatalf("sent media photo = %#v, want photo %d", photoMedia.Photo, f.photo.ID)
	}
}

func TestInlineBotDocumentMediaChannelRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	created, err := f.channels.CreateMegagroupFromCreateChat(ctx, f.owner.ID, domain.CreateChannelRequest{
		Title:         "Inline Document Group",
		MemberUserIDs: []int64{f.peer.ID},
		Date:          1700002000,
	})
	if err != nil {
		t.Fatalf("create inline document group: %v", err)
	}
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, &tg.MessagesGetInlineBotResultsRequest{
			Bot:   inputUser(f.bot),
			Peer:  peer,
			Query: "document",
		})
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			inlineDocumentResult("doc-1", f.document, "inline document caption"),
		},
	}); err != nil || !ok {
		t.Fatalf("set inline document result = %v,%v, want true,nil", ok, err)
	}

	select {
	case got := <-gotCh:
		if got.err != nil {
			t.Fatalf("get inline document results: %v", got.err)
		}
		mediaResult, ok := got.res.Results[0].(*tg.BotInlineMediaResult)
		if !ok {
			t.Fatalf("inline document result = %T, want *tg.BotInlineMediaResult", got.res.Results[0])
		}
		if doc, ok := mediaResult.Document.(*tg.Document); !ok || doc.ID != f.document.ID {
			t.Fatalf("inline result document = %#v, want document %d", mediaResult.Document, f.document.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("document get inline results did not resolve")
	}

	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     peer,
		RandomID: 9904,
		QueryID:  queryID,
		ID:       "doc-1",
	})
	if err != nil {
		t.Fatalf("send inline document result: %v", err)
	}
	sent, full := channelMessageFromUpdates(t, updatesClass)
	if sent.Message != "inline document caption" {
		t.Fatalf("sent document caption = %q, want inline document caption", sent.Message)
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("sent document via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
	docMedia, ok := sent.Media.(*tg.MessageMediaDocument)
	if !ok {
		t.Fatalf("sent channel media = %T, want *tg.MessageMediaDocument", sent.Media)
	}
	if doc, ok := docMedia.Document.(*tg.Document); !ok || doc.ID != f.document.ID {
		t.Fatalf("sent channel document = %#v, want document %d", docMedia.Document, f.document.ID)
	}
	assertUpdatesContainBotUser(t, full, f.bot.ID)

	historyList, err := f.router.deps.Channels.GetHistory(ownerCtx, f.owner.ID, domain.ChannelHistoryFilter{
		ChannelID: created.Channel.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel document history: %v", err)
	}
	history := tgChannelHistoryMessages(f.owner.ID, f.router.enrichChannelHistory(ownerCtx, f.owner.ID, historyList))
	channelHistory, ok := history.(*tg.MessagesChannelMessages)
	if !ok || len(channelHistory.Messages) == 0 {
		t.Fatalf("channel document history = %T len %d, want channel messages", history, len(channelMessagesFromClass(history)))
	}
	histMsg, ok := channelHistory.Messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("channel document history message = %T, want *tg.Message", channelHistory.Messages[0])
	}
	if _, ok := histMsg.Media.(*tg.MessageMediaDocument); !ok {
		t.Fatalf("channel document history media = %T, want *tg.MessageMediaDocument", histMsg.Media)
	}
	if via, ok := histMsg.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("channel document history via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
}

func TestInlineBotContactMediaPrivateRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, &tg.MessagesGetInlineBotResultsRequest{
			Bot:   inputUser(f.bot),
			Peer:  inputPeerUser(f.peer),
			Query: "contact",
		})
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			inlineContactResult("contact-1", f.peer.Phone, "Peer", "Shared", "BEGIN:VCARD\nFN:Peer Shared\nEND:VCARD"),
		},
	}); err != nil || !ok {
		t.Fatalf("set inline contact result = %v,%v, want true,nil", ok, err)
	}

	var got getResult
	select {
	case got = <-gotCh:
	case <-time.After(2 * time.Second):
		t.Fatal("contact get inline results did not resolve")
	}
	if got.err != nil {
		t.Fatalf("get inline contact results: %v", got.err)
	}
	result, ok := got.res.Results[0].(*tg.BotInlineResult)
	if !ok {
		t.Fatalf("inline contact result = %T, want *tg.BotInlineResult", got.res.Results[0])
	}
	contactMsg, ok := result.SendMessage.(*tg.BotInlineMessageMediaContact)
	if !ok {
		t.Fatalf("inline contact send_message = %T, want *tg.BotInlineMessageMediaContact", result.SendMessage)
	}
	if contactMsg.PhoneNumber != f.peer.Phone || contactMsg.FirstName != "Peer" || contactMsg.LastName != "Shared" {
		t.Fatalf("inline contact message = %+v, want peer contact", contactMsg)
	}

	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 9905,
		QueryID:  queryID,
		ID:       "contact-1",
	})
	if err != nil {
		t.Fatalf("send inline contact result: %v", err)
	}
	sent := messageFromUpdates(t, updatesClass)
	media, ok := sent.Media.(*tg.MessageMediaContact)
	if !ok {
		t.Fatalf("sent contact media = %T, want *tg.MessageMediaContact", sent.Media)
	}
	if media.PhoneNumber != f.peer.Phone || media.UserID != f.peer.ID || media.Vcard == "" {
		t.Fatalf("sent contact media = %+v, want peer contact with user_id", media)
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("sent contact via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
}

func TestInlineBotContactMediaChannelRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	created, err := f.channels.CreateMegagroupFromCreateChat(ctx, f.owner.ID, domain.CreateChannelRequest{
		Title:         "Inline Contact Group",
		MemberUserIDs: []int64{f.peer.ID},
		Date:          1700002200,
	})
	if err != nil {
		t.Fatalf("create inline contact group: %v", err)
	}
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, &tg.MessagesGetInlineBotResultsRequest{
			Bot:   inputUser(f.bot),
			Peer:  peer,
			Query: "contact",
		})
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			inlineContactResultWithCallback("contact-channel", f.peer.Phone, "Peer", "Shared", []byte("contact-data")),
		},
	}); err != nil || !ok {
		t.Fatalf("set channel inline contact result = %v,%v, want true,nil", ok, err)
	}

	select {
	case got := <-gotCh:
		if got.err != nil {
			t.Fatalf("get channel inline contact results: %v", got.err)
		}
		result, ok := got.res.Results[0].(*tg.BotInlineResult)
		if !ok {
			t.Fatalf("channel inline contact result = %T, want *tg.BotInlineResult", got.res.Results[0])
		}
		msg, ok := result.SendMessage.(*tg.BotInlineMessageMediaContact)
		if !ok {
			t.Fatalf("channel inline contact send_message = %T, want contact", result.SendMessage)
		}
		if markup, ok := msg.GetReplyMarkup(); !ok || markup == nil {
			t.Fatalf("channel inline contact missing reply markup")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel contact get inline results did not resolve")
	}

	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     peer,
		RandomID: 9906,
		QueryID:  queryID,
		ID:       "contact-channel",
	})
	if err != nil {
		t.Fatalf("send channel inline contact result: %v", err)
	}
	sent, full := channelMessageFromUpdates(t, updatesClass)
	media, ok := sent.Media.(*tg.MessageMediaContact)
	if !ok {
		t.Fatalf("sent channel contact media = %T, want *tg.MessageMediaContact", sent.Media)
	}
	if media.PhoneNumber != f.peer.Phone || media.UserID != f.peer.ID {
		t.Fatalf("sent channel contact media = %+v, want peer contact with user_id", media)
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("sent channel contact via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
	assertTGInlineReplyMarkup(t, sent, "Contact", []byte("contact-data"))
	assertUpdatesContainBotUser(t, full, f.bot.ID)

	historyList, err := f.router.deps.Channels.GetHistory(ownerCtx, f.owner.ID, domain.ChannelHistoryFilter{
		ChannelID: created.Channel.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel contact history: %v", err)
	}
	history := tgChannelHistoryMessages(f.owner.ID, f.router.enrichChannelHistory(ownerCtx, f.owner.ID, historyList))
	channelHistory, ok := history.(*tg.MessagesChannelMessages)
	if !ok || len(channelHistory.Messages) == 0 {
		t.Fatalf("channel contact history = %T len %d, want channel messages", history, len(channelMessagesFromClass(history)))
	}
	histMsg, ok := channelHistory.Messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("channel contact history message = %T, want *tg.Message", channelHistory.Messages[0])
	}
	if _, ok := histMsg.Media.(*tg.MessageMediaContact); !ok {
		t.Fatalf("channel contact history media = %T, want *tg.MessageMediaContact", histMsg.Media)
	}
	assertTGInlineReplyMarkup(t, histMsg, "Contact", []byte("contact-data"))

	diff, err := f.router.deps.Channels.GetDifference(ownerCtx, f.owner.ID, domain.ChannelDifferenceRequest{
		UserID:    f.owner.ID,
		ChannelID: created.Channel.ID,
		Pts:       created.Channel.Pts,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel contact difference: %v", err)
	}
	if len(diff.NewMessages) != 1 || diff.NewMessages[0].Media == nil || diff.NewMessages[0].Media.Kind != domain.MessageMediaKindContact {
		t.Fatalf("channel contact difference messages = %+v, want one contact", diff.NewMessages)
	}
	assertDomainInlineReplyMarkup(t, diff.NewMessages[0].ReplyMarkup, "Contact", []byte("contact-data"))
}

func TestInlineBotContactValidation(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	botCtx := WithUserID(ctx, f.bot.ID)

	queryID, _ := f.router.inlines.register(f.router.clock.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID})
	defer f.router.inlines.consume(queryID)

	longVcard := strings.Repeat("x", maxContactVcardLength+1)
	withThumb := inlineContactResult("contact-thumb", f.peer.Phone, "Peer", "Shared", "")
	withThumb.SetThumb(tg.InputWebDocument{URL: "https://example.test/thumb.jpg", Size: 128, MimeType: "image/jpeg"})
	cases := []struct {
		name   string
		result tg.InputBotInlineResultClass
		want   string
	}{
		{
			name: "type mismatch",
			result: func() tg.InputBotInlineResultClass {
				r := inlineContactResult("bad-type", f.peer.Phone, "Peer", "", "")
				r.Type = "article"
				return r
			}(),
			want: "RESULT_TYPE_INVALID",
		},
		{
			name:   "empty contact",
			result: inlineContactResult("empty-contact", "", "", "", ""),
			want:   "MEDIA_EMPTY",
		},
		{
			name:   "vcard too long",
			result: inlineContactResult("long-vcard", f.peer.Phone, "Peer", "", longVcard),
			want:   "MEDIA_INVALID",
		},
		{
			name:   "web preview unsupported",
			result: withThumb,
			want:   "WEBDOCUMENT_INVALID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
				QueryID: queryID,
				Results: []tg.InputBotInlineResultClass{
					tc.result,
				},
			}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("set inline contact err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestInlineBotGeoMediaPrivateRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	req := &tg.MessagesGetInlineBotResultsRequest{
		Bot:   inputUser(f.bot),
		Peer:  inputPeerUser(f.peer),
		Query: "geo",
	}
	req.SetGeoPoint(&tg.InputGeoPoint{Lat: 39.9042, Long: 116.4074})
	if _, err := f.router.onMessagesGetInlineBotResults(ownerCtx, req); err == nil || !strings.Contains(err.Error(), "BOT_INLINE_GEO_NOT_ALLOWED") {
		t.Fatalf("inline geo disabled err = %v, want BOT_INLINE_GEO_NOT_ALLOWED", err)
	}
	if _, err := f.bots.SetInlineGeo(ctx, f.bot.ID, true); err != nil {
		t.Fatalf("enable inline geo: %v", err)
	}

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, req)
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			inlineGeoResult("geo-1", 31.2304, 121.4737),
		},
	}); err != nil || !ok {
		t.Fatalf("set inline geo result = %v,%v, want true,nil", ok, err)
	}

	var got getResult
	select {
	case got = <-gotCh:
	case <-time.After(2 * time.Second):
		t.Fatal("geo get inline results did not resolve")
	}
	if got.err != nil {
		t.Fatalf("get inline geo results: %v", got.err)
	}
	if len(got.res.Users) != 1 {
		t.Fatalf("geo inline users = %d, want bot user", len(got.res.Users))
	}
	botUser, ok := got.res.Users[0].(*tg.User)
	if !ok || !botUser.BotInlineGeo {
		t.Fatalf("geo inline bot user = %#v, want bot_inline_geo", got.res.Users[0])
	}
	result, ok := got.res.Results[0].(*tg.BotInlineResult)
	if !ok {
		t.Fatalf("geo inline result = %T, want *tg.BotInlineResult", got.res.Results[0])
	}
	if _, ok := result.SendMessage.(*tg.BotInlineMessageMediaGeo); !ok {
		t.Fatalf("geo send_message = %T, want *tg.BotInlineMessageMediaGeo", result.SendMessage)
	}

	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 9905,
		QueryID:  queryID,
		ID:       "geo-1",
	})
	if err != nil {
		t.Fatalf("send inline geo result: %v", err)
	}
	sent := messageFromUpdates(t, updatesClass)
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("sent geo via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
	media, ok := sent.Media.(*tg.MessageMediaGeo)
	if !ok {
		t.Fatalf("sent geo media = %T, want *tg.MessageMediaGeo", sent.Media)
	}
	point, ok := media.Geo.(*tg.GeoPoint)
	if !ok || point.Lat != 31.2304 || point.Long != 121.4737 {
		t.Fatalf("sent geo point = %#v, want 31.2304,121.4737", media.Geo)
	}
}

func TestInlineBotVenueMediaChannelRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	if _, err := f.bots.SetInlineGeo(ctx, f.bot.ID, true); err != nil {
		t.Fatalf("enable inline geo: %v", err)
	}
	created, err := f.channels.CreateMegagroupFromCreateChat(ctx, f.owner.ID, domain.CreateChannelRequest{
		Title:         "Inline Venue Group",
		MemberUserIDs: []int64{f.peer.ID},
		Date:          1700003000,
	})
	if err != nil {
		t.Fatalf("create inline venue group: %v", err)
	}
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	req := &tg.MessagesGetInlineBotResultsRequest{
		Bot:   inputUser(f.bot),
		Peer:  peer,
		Query: "venue",
	}
	req.SetGeoPoint(&tg.InputGeoPoint{Lat: 39.9042, Long: 116.4074})

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, req)
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			inlineVenueResult("venue-1", "Cafe Inline", 30.2672, -97.7431),
		},
	}); err != nil || !ok {
		t.Fatalf("set inline venue result = %v,%v, want true,nil", ok, err)
	}

	select {
	case got := <-gotCh:
		if got.err != nil {
			t.Fatalf("get inline venue results: %v", got.err)
		}
		result, ok := got.res.Results[0].(*tg.BotInlineResult)
		if !ok {
			t.Fatalf("venue inline result = %T, want *tg.BotInlineResult", got.res.Results[0])
		}
		if _, ok := result.SendMessage.(*tg.BotInlineMessageMediaVenue); !ok {
			t.Fatalf("venue send_message = %T, want *tg.BotInlineMessageMediaVenue", result.SendMessage)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("venue get inline results did not resolve")
	}

	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     peer,
		RandomID: 9906,
		QueryID:  queryID,
		ID:       "venue-1",
	})
	if err != nil {
		t.Fatalf("send inline venue result: %v", err)
	}
	sent, full := channelMessageFromUpdates(t, updatesClass)
	venueMedia, ok := sent.Media.(*tg.MessageMediaVenue)
	if !ok {
		t.Fatalf("sent venue media = %T, want *tg.MessageMediaVenue", sent.Media)
	}
	if venueMedia.Title != "Cafe Inline" {
		t.Fatalf("sent venue title = %q, want Cafe Inline", venueMedia.Title)
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("sent venue via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
	assertUpdatesContainBotUser(t, full, f.bot.ID)

	historyList, err := f.router.deps.Channels.GetHistory(ownerCtx, f.owner.ID, domain.ChannelHistoryFilter{
		ChannelID: created.Channel.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel venue history: %v", err)
	}
	history := tgChannelHistoryMessages(f.owner.ID, f.router.enrichChannelHistory(ownerCtx, f.owner.ID, historyList))
	channelHistory, ok := history.(*tg.MessagesChannelMessages)
	if !ok || len(channelHistory.Messages) == 0 {
		t.Fatalf("channel venue history = %T len %d, want channel messages", history, len(channelMessagesFromClass(history)))
	}
	histMsg, ok := channelHistory.Messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("channel venue history message = %T, want *tg.Message", channelHistory.Messages[0])
	}
	if _, ok := histMsg.Media.(*tg.MessageMediaVenue); !ok {
		t.Fatalf("channel venue history media = %T, want *tg.MessageMediaVenue", histMsg.Media)
	}
	if via, ok := histMsg.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("channel venue history via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
}

func TestInlineBotSwitchPMRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	type getResult struct {
		res *tg.MessagesBotResults
		err error
	}
	gotCh := make(chan getResult, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, &tg.MessagesGetInlineBotResultsRequest{
			Bot:   inputUser(f.bot),
			Peer:  inputPeerUser(f.peer),
			Query: "connect",
		})
		gotCh <- getResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	req := &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results:   []tg.InputBotInlineResultClass{},
	}
	req.SetSwitchPm(tg.InlineBotSwitchPM{Text: "Connect account", StartParam: "connect_123"})
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, req); err != nil || !ok {
		t.Fatalf("set switch_pm inline results = %v,%v, want true,nil", ok, err)
	}

	var got getResult
	select {
	case got = <-gotCh:
	case <-time.After(2 * time.Second):
		t.Fatal("switch_pm get inline results did not resolve")
	}
	if got.err != nil {
		t.Fatalf("get switch_pm inline results: %v", got.err)
	}
	if got.res.QueryID != queryID {
		t.Fatalf("switch_pm query_id = %d, want %d", got.res.QueryID, queryID)
	}
	if len(got.res.Results) != 0 {
		t.Fatalf("switch_pm results len = %d, want 0", len(got.res.Results))
	}
	switchPM, ok := got.res.GetSwitchPm()
	if !ok {
		t.Fatal("switch_pm missing in bot results")
	}
	if switchPM.Text != "Connect account" || switchPM.StartParam != "connect_123" {
		t.Fatalf("switch_pm = %#v, want text/start_param", switchPM)
	}
	if len(got.res.Users) != 1 {
		t.Fatalf("switch_pm users = %d, want bot user", len(got.res.Users))
	}
}

func TestInlineBotServerCacheHitUsesFreshQueryID(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)
	req := &tg.MessagesGetInlineBotResultsRequest{
		Bot:   inputUser(f.bot),
		Peer:  inputPeerUser(f.peer),
		Query: "cached",
	}

	gotCh := make(chan struct {
		res *tg.MessagesBotResults
		err error
	}, 1)
	go func() {
		res, err := f.router.onMessagesGetInlineBotResults(ownerCtx, req)
		gotCh <- struct {
			res *tg.MessagesBotResults
			err error
		}{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:    queryID,
		CacheTime:  60,
		NextOffset: "page2",
		Results:    []tg.InputBotInlineResultClass{inlineArticleResult("cached-1", "cached hello")},
	}); err != nil || !ok {
		t.Fatalf("set cached inline results = %v,%v, want true,nil", ok, err)
	}
	select {
	case got := <-gotCh:
		if got.err != nil {
			t.Fatalf("first cached get: %v", got.err)
		}
		if got.res.QueryID != queryID {
			t.Fatalf("first query_id = %d, want %d", got.res.QueryID, queryID)
		}
		if next, ok := got.res.GetNextOffset(); !ok || next != "page2" {
			t.Fatalf("first next_offset = %q,%v, want page2,true", next, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first cached get did not resolve")
	}
	if f.router.inlines.unansweredSize() != 0 {
		t.Fatalf("unanswered inline queries after first answer = %d, want 0", f.router.inlines.unansweredSize())
	}

	fastCtx, cancel := context.WithTimeout(ownerCtx, 200*time.Millisecond)
	defer cancel()
	cached, err := f.router.onMessagesGetInlineBotResults(fastCtx, req)
	if err != nil {
		t.Fatalf("cached get: %v", err)
	}
	if cached.QueryID == queryID {
		t.Fatalf("cached query_id reused old value %d", queryID)
	}
	if f.router.inlines.unansweredSize() != 0 {
		t.Fatalf("unanswered inline queries after cache hit = %d, want 0", f.router.inlines.unansweredSize())
	}
	if next, ok := cached.GetNextOffset(); !ok || next != "page2" {
		t.Fatalf("cached next_offset = %q,%v, want page2,true", next, ok)
	}

	updatesClass, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer:     inputPeerUser(f.peer),
		RandomID: 9910,
		QueryID:  cached.QueryID,
		ID:       "cached-1",
	})
	if err != nil {
		t.Fatalf("send cached inline result: %v", err)
	}
	sent := messageFromUpdates(t, updatesClass)
	if sent.Message != "cached hello" {
		t.Fatalf("cached sent message = %q, want cached hello", sent.Message)
	}
	if via, ok := sent.GetViaBotID(); !ok || via != f.bot.ID {
		t.Fatalf("cached via_bot_id = %d,%v, want %d,true", via, ok, f.bot.ID)
	}
}

func TestInlineBotServerCacheSkipsPrivateAndZeroCache(t *testing.T) {
	for _, tc := range []struct {
		name      string
		private   bool
		cacheTime int
	}{
		{name: "private", private: true, cacheTime: 60},
		{name: "zero cache", private: false, cacheTime: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newInlineBotRPCTestFixture(t)
			ownerCtx := WithUserID(ctx, f.owner.ID)
			botCtx := WithUserID(ctx, f.bot.ID)
			req := &tg.MessagesGetInlineBotResultsRequest{
				Bot:   inputUser(f.bot),
				Peer:  inputPeerUser(f.peer),
				Query: tc.name,
			}

			firstCh := make(chan error, 1)
			go func() {
				_, err := f.router.onMessagesGetInlineBotResults(ownerCtx, req)
				firstCh <- err
			}()
			firstID := waitInlineBotQuery(t, f.router)
			setReq := &tg.MessagesSetInlineBotResultsRequest{
				Private:   tc.private,
				QueryID:   firstID,
				CacheTime: tc.cacheTime,
				Results:   []tg.InputBotInlineResultClass{inlineArticleResult("miss-1", "first")},
			}
			if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, setReq); err != nil || !ok {
				t.Fatalf("set first no-cache results = %v,%v, want true,nil", ok, err)
			}
			select {
			case err := <-firstCh:
				if err != nil {
					t.Fatalf("first no-cache get: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("first no-cache get did not resolve")
			}

			secondCh := make(chan error, 1)
			go func() {
				_, err := f.router.onMessagesGetInlineBotResults(ownerCtx, req)
				secondCh <- err
			}()
			secondID := waitInlineBotQuery(t, f.router)
			if secondID == firstID {
				t.Fatalf("second query reused first id %d", firstID)
			}
			if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
				QueryID:   secondID,
				CacheTime: 1,
				Results:   []tg.InputBotInlineResultClass{inlineArticleResult("miss-2", "second")},
			}); err != nil || !ok {
				t.Fatalf("set second no-cache results = %v,%v, want true,nil", ok, err)
			}
			select {
			case err := <-secondCh:
				if err != nil {
					t.Fatalf("second no-cache get: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("second no-cache get did not resolve")
			}
		})
	}
}

func TestInlineBotServerCacheSeparatesOffsets(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	firstReq := &tg.MessagesGetInlineBotResultsRequest{
		Bot:   inputUser(f.bot),
		Peer:  inputPeerUser(f.peer),
		Query: "pages",
	}
	firstCh := make(chan error, 1)
	go func() {
		_, err := f.router.onMessagesGetInlineBotResults(ownerCtx, firstReq)
		firstCh <- err
	}()
	firstID := waitInlineBotQuery(t, f.router)
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:    firstID,
		CacheTime:  60,
		NextOffset: "page2",
		Results:    []tg.InputBotInlineResultClass{inlineArticleResult("page-1", "page one")},
	}); err != nil || !ok {
		t.Fatalf("set first page results = %v,%v, want true,nil", ok, err)
	}
	select {
	case err := <-firstCh:
		if err != nil {
			t.Fatalf("first page get: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first page get did not resolve")
	}

	secondReq := &tg.MessagesGetInlineBotResultsRequest{
		Bot:    inputUser(f.bot),
		Peer:   inputPeerUser(f.peer),
		Query:  "pages",
		Offset: "page2",
	}
	secondCh := make(chan error, 1)
	go func() {
		_, err := f.router.onMessagesGetInlineBotResults(ownerCtx, secondReq)
		secondCh <- err
	}()
	secondID := waitInlineBotQuery(t, f.router)
	if secondID == firstID {
		t.Fatalf("second page reused first id %d", firstID)
	}
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   secondID,
		CacheTime: 60,
		Results:   []tg.InputBotInlineResultClass{inlineArticleResult("page-2", "page two")},
	}); err != nil || !ok {
		t.Fatalf("set second page results = %v,%v, want true,nil", ok, err)
	}
	select {
	case err := <-secondCh:
		if err != nil {
			t.Fatalf("second page get: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second page get did not resolve")
	}
}

func TestInlineBotValidation(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	queryID, _ := f.router.inlines.register(time.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID})

	if _, err := f.router.onMessagesSetInlineBotResults(WithUserID(ctx, f.owner.ID), &tg.MessagesSetInlineBotResultsRequest{
		QueryID: queryID,
	}); err == nil || !strings.Contains(err.Error(), "USER_BOT_REQUIRED") {
		t.Fatalf("non-bot set inline err = %v, want USER_BOT_REQUIRED", err)
	}

	if _, err := f.router.onMessagesSetInlineBotResults(WithUserID(ctx, f.bot.ID), &tg.MessagesSetInlineBotResultsRequest{
		QueryID: queryID,
		Results: []tg.InputBotInlineResultClass{
			inlineArticleResult("dup", "one"),
			inlineArticleResult("dup", "two"),
		},
	}); err == nil || !strings.Contains(err.Error(), "RESULT_ID_DUPLICATE") {
		t.Fatalf("duplicate inline result err = %v, want RESULT_ID_DUPLICATE", err)
	}

	disabledBot, _, err := f.bots.CreateBot(ctx, f.owner.ID, "Disabled Inline Bot", "disabled_inline_bot")
	if err != nil {
		t.Fatalf("create disabled bot: %v", err)
	}
	if _, err := f.router.onMessagesGetInlineBotResults(WithUserID(ctx, f.owner.ID), &tg.MessagesGetInlineBotResultsRequest{
		Bot:   inputUser(disabledBot),
		Peer:  inputPeerUser(f.peer),
		Query: "hello",
	}); err == nil || !strings.Contains(err.Error(), "BOT_INLINE_DISABLED") {
		t.Fatalf("disabled inline err = %v, want BOT_INLINE_DISABLED", err)
	}

	if _, err := f.bots.SetInlineGeo(ctx, f.bot.ID, true); err != nil {
		t.Fatalf("enable inline geo: %v", err)
	}
	badGeoReq := &tg.MessagesGetInlineBotResultsRequest{
		Bot:   inputUser(f.bot),
		Peer:  inputPeerUser(f.peer),
		Query: "bad geo",
	}
	badGeoReq.SetGeoPoint(&tg.InputGeoPoint{Lat: 91, Long: 0})
	if _, err := f.router.onMessagesGetInlineBotResults(WithUserID(ctx, f.owner.ID), badGeoReq); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
		t.Fatalf("bad inline geo query err = %v, want MEDIA_INVALID", err)
	}

	for _, tc := range []struct {
		name   string
		result tg.InputBotInlineResultClass
		want   string
	}{
		{name: "geo live fields", result: inlineGeoResultWithPeriod("geo-live", 1, 2), want: "MEDIA_INVALID"},
		{name: "empty venue title", result: inlineVenueResult("venue-empty", "", 1, 2), want: "MEDIA_EMPTY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queryID, _ := f.router.inlines.register(time.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID})
			if _, err := f.router.onMessagesSetInlineBotResults(WithUserID(ctx, f.bot.ID), &tg.MessagesSetInlineBotResultsRequest{
				QueryID: queryID,
				Results: []tg.InputBotInlineResultClass{
					tc.result,
				},
			}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("inline media validation err = %v, want %s", err, tc.want)
			}
		})
	}

	switchPMCases := []struct {
		name string
		pm   tg.InlineBotSwitchPM
		want string
	}{
		{name: "empty text", pm: tg.InlineBotSwitchPM{Text: "", StartParam: "start"}, want: "SWITCH_PM_TEXT_EMPTY"},
		{name: "empty start", pm: tg.InlineBotSwitchPM{Text: "Start", StartParam: ""}, want: "START_PARAM_EMPTY"},
		{name: "invalid start", pm: tg.InlineBotSwitchPM{Text: "Start", StartParam: "bad space"}, want: "START_PARAM_INVALID"},
		{name: "long start", pm: tg.InlineBotSwitchPM{Text: "Start", StartParam: strings.Repeat("a", domain.MaxStartParamLen+1)}, want: "START_PARAM_INVALID"},
		{name: "long text", pm: tg.InlineBotSwitchPM{Text: strings.Repeat("x", domain.MaxBotInlineSwitchTextLen+1), StartParam: "start"}, want: "BUTTON_INVALID"},
	}
	for _, tc := range switchPMCases {
		t.Run(tc.name, func(t *testing.T) {
			queryID, _ := f.router.inlines.register(time.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID})
			req := &tg.MessagesSetInlineBotResultsRequest{QueryID: queryID}
			req.SetSwitchPm(tc.pm)
			if _, err := f.router.onMessagesSetInlineBotResults(WithUserID(ctx, f.bot.ID), req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("switch_pm err = %v, want %s", err, tc.want)
			}
		})
	}

	webviewQueryID, _ := f.router.inlines.register(time.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID})
	webviewReq := &tg.MessagesSetInlineBotResultsRequest{QueryID: webviewQueryID}
	webviewReq.SetSwitchWebview(tg.InlineBotWebView{Text: "Open", URL: "https://example.test/app"})
	if ok, err := f.router.onMessagesSetInlineBotResults(WithUserID(ctx, f.bot.ID), webviewReq); err != nil || !ok {
		t.Fatalf("switch_webview set = %v,%v, want true,nil", ok, err)
	}
	results, ok := f.router.inlines.resultsForQueryContext(ctx, time.Now(), f.owner.ID, webviewQueryID)
	if !ok || results.SwitchWeb == nil || results.SwitchWeb.Text != "Open" || results.SwitchWeb.URL != "https://example.test/app" {
		t.Fatalf("switch_webview results = %+v ok=%v", results.SwitchWeb, ok)
	}
	tgResults := f.router.tgBotInlineResults(ctx, f.owner.ID, results)
	gotSwitchWeb, ok := tgResults.GetSwitchWebview()
	if !ok || gotSwitchWeb.Text != "Open" || gotSwitchWeb.URL != "https://example.test/app" {
		t.Fatalf("tg switch_webview = %+v,%v", gotSwitchWeb, ok)
	}

	for _, tc := range []struct {
		name string
		web  tg.InlineBotWebView
		want string
	}{
		{name: "empty text", web: tg.InlineBotWebView{Text: "", URL: "https://example.test/app"}, want: "BUTTON_INVALID"},
		{name: "bad url", web: tg.InlineBotWebView{Text: "Open", URL: "http://example.test/app"}, want: "SWITCH_WEBVIEW_URL_INVALID"},
	} {
		t.Run("switch_webview "+tc.name, func(t *testing.T) {
			queryID, _ := f.router.inlines.register(time.Now(), f.bot.ID, f.owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID})
			req := &tg.MessagesSetInlineBotResultsRequest{QueryID: queryID}
			req.SetSwitchWebview(tc.web)
			if _, err := f.router.onMessagesSetInlineBotResults(WithUserID(ctx, f.bot.ID), req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("switch_webview err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestMessagesRequestSimpleWebViewURLOnly(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	req := &tg.MessagesRequestSimpleWebViewRequest{
		Bot:      inputUser(f.bot),
		Platform: "tdesktop",
	}
	req.SetFromSwitchWebview(true)
	req.SetURL("https://example.test/app")
	req.SetThemeParams(tg.DataJSON{Data: "{}"})

	got, err := f.router.onMessagesRequestSimpleWebView(WithUserID(ctx, f.owner.ID), req)
	if err != nil {
		t.Fatalf("request simple webview: %v", err)
	}
	if got.URL != "https://example.test/app" {
		t.Fatalf("webview url = %q, want request url", got.URL)
	}
	if _, ok := got.GetQueryID(); ok {
		t.Fatal("webview query_id set, want URL-only result")
	}

	req.SetURL("http://example.test/app")
	if _, err := f.router.onMessagesRequestSimpleWebView(WithUserID(ctx, f.owner.ID), req); err == nil || !strings.Contains(err.Error(), "URL_INVALID") {
		t.Fatalf("bad webview url err = %v, want URL_INVALID", err)
	}
	req.SetURL("https://example.test/app")
	req.Bot = inputUser(f.owner)
	if _, err := f.router.onMessagesRequestSimpleWebView(WithUserID(ctx, f.owner.ID), req); err == nil || !strings.Contains(err.Error(), "BOT_INVALID") {
		t.Fatalf("non-bot webview err = %v, want BOT_INVALID", err)
	}
}

func channelMessageFromUpdates(t *testing.T, updates tg.UpdatesClass) (*tg.Message, *tg.Updates) {
	t.Helper()
	full, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	for _, update := range full.Updates {
		if newMessage, ok := update.(*tg.UpdateNewChannelMessage); ok {
			if msg, ok := newMessage.Message.(*tg.Message); ok {
				return msg, full
			}
		}
	}
	t.Fatalf("updates missing new channel message: %+v", full.Updates)
	return nil, nil
}

func assertUpdatesContainBotUser(t *testing.T, updates *tg.Updates, botID int64) {
	t.Helper()
	for _, u := range updates.Users {
		if user, ok := u.(*tg.User); ok && user.ID == botID && user.Bot {
			return
		}
	}
	t.Fatalf("updates users missing via bot %d: %+v", botID, updates.Users)
}

func assertChannelMessagesContainBotUser(t *testing.T, messages *tg.MessagesChannelMessages, botID int64) {
	t.Helper()
	for _, u := range messages.Users {
		if user, ok := u.(*tg.User); ok && user.ID == botID && user.Bot {
			return
		}
	}
	t.Fatalf("channel messages users missing via bot %d: %+v", botID, messages.Users)
}

func assertTGInlineReplyMarkup(t *testing.T, msg *tg.Message, wantText string, wantData []byte) {
	t.Helper()
	markupClass, ok := msg.GetReplyMarkup()
	if !ok {
		t.Fatalf("message %d missing reply_markup", msg.ID)
	}
	markup, ok := markupClass.(*tg.ReplyInlineMarkup)
	if !ok {
		t.Fatalf("reply_markup = %T, want *tg.ReplyInlineMarkup", markupClass)
	}
	if len(markup.Rows) != 1 || len(markup.Rows[0].Buttons) != 1 {
		t.Fatalf("reply_markup rows = %+v, want one callback button", markup.Rows)
	}
	button, ok := markup.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if !ok {
		t.Fatalf("reply_markup button = %T, want callback", markup.Rows[0].Buttons[0])
	}
	if button.Text != wantText || !bytes.Equal(button.Data, wantData) {
		t.Fatalf("reply_markup button = %q/%v, want %q/%v", button.Text, button.Data, wantText, wantData)
	}
}

func assertDomainInlineReplyMarkup(t *testing.T, markup *domain.MessageReplyMarkup, wantText string, wantData []byte) {
	t.Helper()
	if markup == nil || len(markup.Inline) != 1 || len(markup.Inline[0]) != 1 {
		t.Fatalf("domain reply markup = %+v, want one callback button", markup)
	}
	button := markup.Inline[0][0]
	if button.Type != domain.MarkupButtonCallback || button.Text != wantText || !bytes.Equal(button.Data, wantData) {
		t.Fatalf("domain reply markup button = %+v, want callback %q/%v", button, wantText, wantData)
	}
}

func waitInlineBotQuery(t *testing.T, r *Router) int64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.inlines.mu.Lock()
		for queryID, pending := range r.inlines.pending {
			if pending.results == nil {
				r.inlines.mu.Unlock()
				return queryID
			}
		}
		r.inlines.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("inline query was not registered")
	return 0
}

func inputPeerUser(user domain.User) *tg.InputPeerUser {
	return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}
}

func inlineArticleResult(id, message string) tg.InputBotInlineResultClass {
	return &tg.InputBotInlineResult{
		ID:          id,
		Type:        "article",
		Title:       id,
		SendMessage: &tg.InputBotInlineMessageText{Message: message},
	}
}

func inlineArticleResultWithCallback(id, message, button string, data []byte) tg.InputBotInlineResultClass {
	result := inlineArticleResult(id, message).(*tg.InputBotInlineResult)
	msg := result.SendMessage.(*tg.InputBotInlineMessageText)
	msg.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{
		Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonCallback{Text: button, Data: data}},
	}}})
	return result
}

func inlineArticleResultWithWebThumb(id, message string) *tg.InputBotInlineResult {
	result := &tg.InputBotInlineResult{
		ID:          id,
		Type:        "article",
		Title:       "Preview",
		Description: "External preview",
		SendMessage: &tg.InputBotInlineMessageText{Message: message},
	}
	result.SetURL("https://example.test/result")
	result.SetThumb(tg.InputWebDocument{
		URL:      "https://example.test/thumb.jpg",
		Size:     1234,
		MimeType: "image/jpeg",
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeImageSize{W: 96, H: 96},
		},
	})
	return result
}

func inlineArticleResultWithWebContent(id, message string) *tg.InputBotInlineResult {
	result := inlineArticleResultWithWebThumb(id, message)
	result.SetContent(tg.InputWebDocument{
		URL:      "https://example.test/content.png",
		Size:     4096,
		MimeType: "image/png",
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeImageSize{W: 320, H: 200},
		},
	})
	return result
}

func inlineExternalPhotoContentResult(id, caption string) *tg.InputBotInlineResult {
	result := &tg.InputBotInlineResult{
		ID:          id,
		Type:        "photo",
		Title:       "External Photo",
		Description: "External photo content",
		SendMessage: &tg.InputBotInlineMessageMediaAuto{
			Message: caption,
		},
	}
	result.SetThumb(tg.InputWebDocument{
		URL:      "https://example.test/photo-thumb.jpg",
		Size:     512,
		MimeType: "image/jpeg",
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeImageSize{W: 90, H: 90},
		},
	})
	result.SetContent(tg.InputWebDocument{
		URL:      "https://example.test/photo.png",
		Size:     4096,
		MimeType: "image/png",
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeImageSize{W: 320, H: 200},
		},
	})
	return result
}

func assertInlineArticleWebPreview(t *testing.T, res *tg.MessagesBotResults, queryID int64) {
	t.Helper()
	if res.QueryID != queryID || len(res.Results) != 1 {
		t.Fatalf("inline results = query %d len %d, want query %d len 1", res.QueryID, len(res.Results), queryID)
	}
	result, ok := res.Results[0].(*tg.BotInlineResult)
	if !ok {
		t.Fatalf("inline result = %T, want *tg.BotInlineResult", res.Results[0])
	}
	if result.ID != "article-web" || result.Type != "article" {
		t.Fatalf("inline result id/type = %q/%q, want article-web/article", result.ID, result.Type)
	}
	if gotURL, ok := result.GetURL(); !ok || gotURL != "https://example.test/result" {
		t.Fatalf("inline result url = %q,%v, want https://example.test/result,true", gotURL, ok)
	}
	rawThumb, ok := result.GetThumb()
	if !ok {
		t.Fatal("inline result missing thumb")
	}
	thumb, ok := rawThumb.(*tg.WebDocument)
	if !ok {
		t.Fatalf("inline result thumb = %T, want *tg.WebDocument", rawThumb)
	}
	if thumb.URL != "https://example.test/thumb.jpg" || thumb.AccessHash == 0 || thumb.Size != 1234 || thumb.MimeType != "image/jpeg" {
		t.Fatalf("thumb = url %q size %d mime %q", thumb.URL, thumb.Size, thumb.MimeType)
	}
	if len(thumb.Attributes) != 1 {
		t.Fatalf("thumb attributes = %d, want 1", len(thumb.Attributes))
	}
	size, ok := thumb.Attributes[0].(*tg.DocumentAttributeImageSize)
	if !ok || size.W != 96 || size.H != 96 {
		t.Fatalf("thumb size attr = %#v, want 96x96", thumb.Attributes[0])
	}
	msg, ok := result.SendMessage.(*tg.BotInlineMessageText)
	if !ok || msg.Message != "preview text" {
		t.Fatalf("inline send message = %T/%#v, want text preview text", result.SendMessage, result.SendMessage)
	}
}

func inlinePhotoResult(id string, photo domain.Photo, caption string) tg.InputBotInlineResultClass {
	return &tg.InputBotInlineResultPhoto{
		ID:    id,
		Type:  "photo",
		Photo: &tg.InputPhoto{ID: photo.ID, AccessHash: photo.AccessHash},
		SendMessage: &tg.InputBotInlineMessageMediaAuto{
			Message: caption,
		},
	}
}

func inlineDocumentResult(id string, doc domain.Document, caption string) tg.InputBotInlineResultClass {
	return &tg.InputBotInlineResultDocument{
		ID:          id,
		Type:        "file",
		Title:       id,
		Description: doc.MimeType,
		Document:    &tg.InputDocument{ID: doc.ID, AccessHash: doc.AccessHash},
		SendMessage: &tg.InputBotInlineMessageMediaAuto{
			Message: caption,
		},
	}
}

func inlineGeoResult(id string, lat, long float64) tg.InputBotInlineResultClass {
	return &tg.InputBotInlineResult{
		ID:          id,
		Type:        "geo",
		Title:       id,
		Description: "Location",
		SendMessage: &tg.InputBotInlineMessageMediaGeo{
			GeoPoint: &tg.InputGeoPoint{Lat: lat, Long: long},
		},
	}
}

func inlineGeoResultWithPeriod(id string, lat, long float64) tg.InputBotInlineResultClass {
	msg := &tg.InputBotInlineMessageMediaGeo{GeoPoint: &tg.InputGeoPoint{Lat: lat, Long: long}}
	msg.SetPeriod(60)
	return &tg.InputBotInlineResult{
		ID:          id,
		Type:        "geo",
		Title:       id,
		SendMessage: msg,
	}
}

func inlineVenueResult(id, title string, lat, long float64) tg.InputBotInlineResultClass {
	return &tg.InputBotInlineResult{
		ID:          id,
		Type:        "venue",
		Title:       title,
		Description: "Venue",
		SendMessage: &tg.InputBotInlineMessageMediaVenue{
			GeoPoint:  &tg.InputGeoPoint{Lat: lat, Long: long},
			Title:     title,
			Address:   "Inline Street",
			Provider:  "gplaces",
			VenueID:   "venue-id",
			VenueType: "cafe",
		},
	}
}

func inlineContactResult(id, phone, first, last, vcard string) *tg.InputBotInlineResult {
	return &tg.InputBotInlineResult{
		ID:          id,
		Type:        "contact",
		Title:       first,
		Description: phone,
		SendMessage: &tg.InputBotInlineMessageMediaContact{
			PhoneNumber: phone,
			FirstName:   first,
			LastName:    last,
			Vcard:       vcard,
		},
	}
}

func inlineContactResultWithCallback(id, phone, first, last string, data []byte) tg.InputBotInlineResultClass {
	result := inlineContactResult(id, phone, first, last, "BEGIN:VCARD\nFN:"+first+" "+last+"\nEND:VCARD")
	msg := result.SendMessage.(*tg.InputBotInlineMessageMediaContact)
	msg.SetReplyMarkup(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{
		Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonCallback{Text: "Contact", Data: data}},
	}}})
	return result
}

func messageFromUpdates(t *testing.T, updates tg.UpdatesClass) *tg.Message {
	t.Helper()
	full, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	for _, update := range full.Updates {
		if newMessage, ok := update.(*tg.UpdateNewMessage); ok {
			if msg, ok := newMessage.Message.(*tg.Message); ok {
				return msg
			}
		}
	}
	t.Fatalf("updates missing new message: %+v", full.Updates)
	return nil
}

func messagesFromClass(in tg.MessagesMessagesClass) []tg.MessageClass {
	if out, ok := in.(*tg.MessagesMessages); ok {
		return out.Messages
	}
	return nil
}

func channelMessagesFromClass(in tg.MessagesMessagesClass) []tg.MessageClass {
	if out, ok := in.(*tg.MessagesChannelMessages); ok {
		return out.Messages
	}
	return nil
}
