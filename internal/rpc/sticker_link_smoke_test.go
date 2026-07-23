package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	botsapp "telesrv/internal/app/bots"
	appmessages "telesrv/internal/app/messages"
	apppolls "telesrv/internal/app/polls"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	publicweb "telesrv/internal/web"
)

func newStickerLinkHandler(t *testing.T, files publicweb.StickerSetResolver) http.Handler {
	t.Helper()
	h, err := publicweb.NewHandler(publicweb.Config{StickerSets: files, PublicBaseURL: "https://telesrv.net"})
	if err != nil {
		t.Fatalf("new public Web handler: %v", err)
	}
	return h
}

func TestCustomStickerPackLinkInstallAndSendSmoke(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550009001", FirstName: "Alice"})
	bob, _ := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550009002", FirstName: "Bob"})
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	pollStore := memory.NewPollStore()
	messageStore.AttachPollStore(pollStore)
	passwordStore := memory.NewPasswordStore()
	files := &fakeFiles{
		docs: map[int64]domain.Document{
			101: {
				ID:         101,
				AccessHash: 1101,
				DCID:       2,
				MimeType:   "image/webp",
				Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}},
			},
		},
		photos: map[int64]domain.Photo{},
		sets:   map[domain.StickerSetKind][]domain.StickerSet{},
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Account:  appaccount.NewService(passwordStore, appaccount.WithUserStickerSets(passwordStore)),
		Users:    appusers.NewService(userStore),
		Messages: appmessages.NewService(messageStore, dialogStore),
		Files:    files,
		Polls:    apppolls.NewService(pollStore),
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)

	created, err := r.onStickersCreateStickerSet(WithUserID(ctx, alice.ID), &tg.StickersCreateStickerSetRequest{
		UserID:    &tg.InputUserSelf{},
		Title:     "Alice Fresh Pack",
		ShortName: "alice_fresh_pack",
		Stickers: []tg.InputStickerSetItem{{
			Document: &tg.InputDocument{ID: 101, AccessHash: 1101},
			Emoji:    "🙂",
			Keywords: "fresh",
		}},
	})
	if err != nil {
		t.Fatalf("create sticker set: %v", err)
	}
	createdFull, ok := created.(*tg.MessagesStickerSet)
	if !ok {
		t.Fatalf("created = %T, want *tg.MessagesStickerSet", created)
	}

	web := newStickerLinkHandler(t, files)
	rr := httptest.NewRecorder()
	web.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/addstickers/alice_fresh_pack", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("sticker link status = %d body=%q, want 200", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"https://telesrv.net/addstickers/alice_fresh_pack", "telesrv://addstickers?set=alice_fresh_pack", "tg://addstickers?set=alice_fresh_pack"} {
		if !strings.Contains(body, want) {
			t.Fatalf("sticker link body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `window.location.href = "tg://`) {
		t.Fatalf("sticker link must auto-open telesrv://, not tg://:\n%s", body)
	}

	preview, err := r.onMessagesGetStickerSet(WithUserID(ctx, bob.ID), &tg.MessagesGetStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "alice_fresh_pack"},
		Hash:       0,
	})
	if err != nil {
		t.Fatalf("bob preview sticker set: %v", err)
	}
	previewFull, ok := preview.(*tg.MessagesStickerSet)
	if !ok || previewFull.Set.ID != createdFull.Set.ID || len(previewFull.Documents) != 1 {
		t.Fatalf("preview = %T %+v, want created set with one document", preview, preview)
	}

	if _, err := r.onMessagesInstallStickerSet(WithUserID(ctx, bob.ID), &tg.MessagesInstallStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "alice_fresh_pack"},
	}); err != nil {
		t.Fatalf("bob install sticker set: %v", err)
	}
	if got := installedStickerSetIDs(t, passwordStore, ctx, bob.ID, domain.StickerSetKindStickers, nil); len(got) != 1 || got[0] != createdFull.Set.ID {
		t.Fatalf("bob installed sets = %v, want [%d]", got, createdFull.Set.ID)
	}

	if _, err := r.onMessagesSendMedia(WithUserID(ctx, bob.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
		Media:    &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 101, AccessHash: 1101}},
		RandomID: 7001,
	}); err != nil {
		t.Fatalf("bob send sticker: %v", err)
	}

	historyReq := &tg.MessagesGetHistoryRequest{
		Peer:  &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Limit: 10,
	}
	var raw bin.Buffer
	if err := historyReq.Encode(&raw); err != nil {
		t.Fatalf("encode history request: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, alice.ID), [8]byte{}, 0, &raw)
	if err != nil {
		t.Fatalf("alice get history: %v", err)
	}
	messages, ok := enc.(*tg.MessagesMessages)
	if !ok {
		t.Fatalf("history response = %T, want *tg.MessagesMessages", enc)
	}
	if len(messages.Messages) != 1 {
		t.Fatalf("history messages = %d, want 1", len(messages.Messages))
	}
	msg, ok := messages.Messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("history message = %T, want *tg.Message", messages.Messages[0])
	}
	media, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok {
		t.Fatalf("history media = %T, want *tg.MessageMediaDocument", msg.Media)
	}
	if got := tgDocumentID(media.Document); got != 101 {
		t.Fatalf("history document id = %d, want 101", got)
	}
}

func TestStickersBotCreatePackLinkInstallIsolationSmoke(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, _ := userStore.Create(ctx, domain.User{AccessHash: 21, Phone: "15550009101", FirstName: "Alice"})
	bob, _ := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550009102", FirstName: "Bob"})
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	pollStore := memory.NewPollStore()
	messageStore.AttachPollStore(pollStore)
	passwordStore := memory.NewPasswordStore()
	accountService := appaccount.NewService(passwordStore, appaccount.WithUserStickerSets(passwordStore))
	botStore := memory.NewBotStore(userStore)
	files := &fakeFiles{
		docs: map[int64]domain.Document{
			401: {
				ID:         401,
				AccessHash: 4401,
				DCID:       2,
				MimeType:   "image/webp",
				Size:       4096,
				Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "alice.webp"}},
			},
		},
		photos: map[int64]domain.Photo{},
		sets:   map[domain.StickerSetKind][]domain.StickerSet{},
	}
	botsService := botsapp.NewService(userStore, botStore, messageStore,
		botsapp.WithStickerSetCreator(files),
		botsapp.WithUserStickerSets(accountService))
	messagesService := appmessages.NewService(messageStore, dialogStore,
		appmessages.WithBotResponder(botsService))
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Account:  accountService,
		Users:    appusers.NewService(userStore),
		Messages: messagesService,
		Files:    files,
		Polls:    apppolls.NewService(pollStore),
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)
	botsService.SetRouterHooks(r)
	botsService.SetTextDraftPusher(r)

	sendStickersBotText(t, r, alice, "/newpack", 9101)
	lastBotReplyID := waitForStickersReplyAfter(t, messageStore, alice.ID, 0, "sticker pack")
	sendStickersBotText(t, r, alice, "Alice Bot Pack", 9102)
	lastBotReplyID = waitForStickersReplyAfter(t, messageStore, alice.ID, lastBotReplyID, "Lottie JSON")
	sendStickersBotDocument(t, r, alice, 401, 4401, 9103)
	lastBotReplyID = waitForStickersReplyAfter(t, messageStore, alice.ID, lastBotReplyID, "emoji")
	sendStickersBotText(t, r, alice, "🙂", 9104)
	lastBotReplyID = waitForStickersReplyAfter(t, messageStore, alice.ID, lastBotReplyID, "Added")
	sendStickersBotText(t, r, alice, "/publish", 9105)
	lastBotReplyID = waitForStickersReplyAfter(t, messageStore, alice.ID, lastBotReplyID, "short name")
	sendStickersBotText(t, r, alice, "alice_bot_pack", 9106)
	waitForStickersReplyAfter(t, messageStore, alice.ID, lastBotReplyID, "https://telesrv.net/addstickers/alice_bot_pack")

	created := files.sets[domain.StickerSetKindStickers]
	if len(created) != 1 || created[0].ShortName != "alice_bot_pack" || created[0].CreatorUserID != alice.ID {
		t.Fatalf("created sets = %+v, want Alice alice_bot_pack", created)
	}
	setID := created[0].ID
	if got := installedStickerSetIDs(t, passwordStore, ctx, alice.ID, domain.StickerSetKindStickers, nil); len(got) != 1 || got[0] != setID {
		t.Fatalf("alice installed sets = %v, want [%d]", got, setID)
	}
	if got := installedStickerSetIDs(t, passwordStore, ctx, bob.ID, domain.StickerSetKindStickers, nil); len(got) != 0 {
		t.Fatalf("bob installed sets before link = %v, want empty", got)
	}
	if got := allStickerSetIDs(t, r, WithUserID(ctx, bob.ID), domain.StickerSetKindStickers); len(got) != 0 {
		t.Fatalf("bob getAllStickers before install = %v, want empty", got)
	}

	web := newStickerLinkHandler(t, files)
	rr := httptest.NewRecorder()
	web.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/addstickers/alice_bot_pack", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "https://telesrv.net/addstickers/alice_bot_pack") {
		t.Fatalf("sticker bot link response = %d %q", rr.Code, rr.Body.String())
	}
	preview, err := r.onMessagesGetStickerSet(WithUserID(ctx, bob.ID), &tg.MessagesGetStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "alice_bot_pack"},
	})
	if err != nil {
		t.Fatalf("bob preview bot-created sticker set: %v", err)
	}
	previewFull, ok := preview.(*tg.MessagesStickerSet)
	if !ok || previewFull.Set.ID != setID || previewFull.Set.InstalledDate != 0 {
		t.Fatalf("bob preview = %T %+v, want uninstalled created set", preview, preview)
	}
	if _, err := r.onMessagesInstallStickerSet(WithUserID(ctx, bob.ID), &tg.MessagesInstallStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "alice_bot_pack"},
	}); err != nil {
		t.Fatalf("bob install bot-created sticker set: %v", err)
	}
	if got := installedStickerSetIDs(t, passwordStore, ctx, bob.ID, domain.StickerSetKindStickers, nil); len(got) != 1 || got[0] != setID {
		t.Fatalf("bob installed sets after link = %v, want [%d]", got, setID)
	}
	if got := allStickerSetIDs(t, r, WithUserID(ctx, alice.ID), domain.StickerSetKindStickers); len(got) != 1 || got[0] != setID {
		t.Fatalf("alice getAllStickers = %v, want [%d]", got, setID)
	}
	if got := allStickerSetIDs(t, r, WithUserID(ctx, bob.ID), domain.StickerSetKindStickers); len(got) != 1 || got[0] != setID {
		t.Fatalf("bob getAllStickers after install = %v, want [%d]", got, setID)
	}
	if got := allStickerSetIDs(t, r, WithUserID(ctx, bob.ID), domain.StickerSetKindEmoji); len(got) != 0 {
		t.Fatalf("bob getEmojiStickers = %v, want empty", got)
	}
}

func sendStickersBotText(t *testing.T, r *Router, user domain.User, text string, randomID int64) {
	t.Helper()
	if _, err := r.onMessagesSendMessage(WithUserID(context.Background(), user.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: domain.StickersBotUserID, AccessHash: domain.StickersBotAccessHash},
		Message:  text,
		RandomID: randomID,
	}); err != nil {
		t.Fatalf("send @Stickers text %q: %v", text, err)
	}
}

func sendStickersBotDocument(t *testing.T, r *Router, user domain.User, docID, accessHash, randomID int64) {
	t.Helper()
	if _, err := r.onMessagesSendMedia(WithUserID(context.Background(), user.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: domain.StickersBotUserID, AccessHash: domain.StickersBotAccessHash},
		Media:    &tg.InputMediaDocument{ID: &tg.InputDocument{ID: docID, AccessHash: accessHash}},
		RandomID: randomID,
	}); err != nil {
		t.Fatalf("send @Stickers document %d: %v", docID, err)
	}
}

func waitForStickersReplyAfter(t *testing.T, messages *memory.MessageStore, userID int64, afterID int, want string) int {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		list, err := messages.ListByUser(context.Background(), userID, domain.MessageFilter{
			HasPeer: true,
			Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.StickersBotUserID},
			Limit:   100,
		})
		if err != nil {
			t.Fatalf("list @Stickers history: %v", err)
		}
		for _, msg := range list.Messages {
			if msg.ID > afterID && msg.From.ID == domain.StickersBotUserID && strings.Contains(msg.Body, want) {
				return msg.ID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no @Stickers reply after id %d containing %q; history=%+v", afterID, want, list.Messages)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func allStickerSetIDs(t *testing.T, r *Router, ctx context.Context, kind domain.StickerSetKind) []int64 {
	t.Helper()
	var (
		out tg.MessagesAllStickersClass
		err error
	)
	if kind == domain.StickerSetKindEmoji {
		out, err = r.onMessagesGetEmojiStickers(ctx, 0)
	} else {
		out, err = r.onMessagesGetAllStickers(ctx, 0)
	}
	if err != nil {
		t.Fatalf("get sticker sets for kind %s: %v", kind, err)
	}
	full, ok := out.(*tg.MessagesAllStickers)
	if !ok {
		t.Fatalf("get sticker sets for kind %s = %T, want *tg.MessagesAllStickers", kind, out)
	}
	ids := make([]int64, 0, len(full.Sets))
	for _, set := range full.Sets {
		ids = append(ids, set.ID)
	}
	return ids
}
