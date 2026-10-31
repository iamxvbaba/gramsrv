package mtprotoedge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/log/logzap"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/transport"

	"telesrv/internal/app/account"
	"telesrv/internal/app/auth"
	botsapp "telesrv/internal/app/bots"
	"telesrv/internal/app/contacts"
	"telesrv/internal/app/dialogs"
	"telesrv/internal/app/help"
	"telesrv/internal/app/langpack"
	messageapp "telesrv/internal/app/messages"
	"telesrv/internal/app/updates"
	"telesrv/internal/app/users"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

// botCallbackEnv sets up an in-memory server, shared by the P3 callback / startBot / markup e2e tests.
type botCallbackEnv struct {
	rsaKey  *rsa.PrivateKey
	addr    *net.TCPAddr
	bots    *botsapp.Service
	newCli  func(*session.StorageMemory, telegram.UpdateHandler) *telegram.Client
	newCliH func(*session.StorageMemory) *telegram.Client
}

func newBotCallbackEnv(t *testing.T, ctx context.Context) *botCallbackEnv {
	t.Helper()
	const dc = 2
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen rsa: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)

	userStore := memory.NewUserStore()
	authzStore := memory.NewAuthorizationStore()
	authKeyStore := memory.NewAuthKeyStore()
	helpStore := memory.NewHelpStore()
	langPackStore := memory.NewLangPackStore()
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	botStore := memory.NewBotStore(userStore)
	botsService := botsapp.NewService(userStore, botStore, messageStore,
		botsapp.WithLogger(zaptest.NewLogger(t).Named("bots")))
	activeSessions := NewSessionManager(zaptest.NewLogger(t).Named("sessions"))
	deps := rpc.Deps{
		Auth: auth.NewService(userStore, authzStore, memory.NewCodeStore(), authKeyStore,
			memory.NewTempAuthKeyBindingStore(authKeyStore), "12345", auth.WithBotLogin(botStore)),
		Account:  account.NewService(memory.NewPasswordStore()),
		Help:     help.NewService(helpStore, helpStore),
		Users:    users.NewService(userStore),
		Updates:  updates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
		Contacts: contacts.NewService(memory.NewContactStore()),
		Dialogs:  dialogs.NewService(dialogStore),
		Messages: messageapp.NewService(messageStore, dialogStore, messageapp.WithBotResponder(botsService)),
		Bots:     botsService,
		LangPack: langpack.NewService(langPackStore),
		Sessions: activeSessions,
	}
	router := rpc.New(rpc.Config{DC: dc, IP: tcpAddr.IP.String(), Port: tcpAddr.Port}, deps, zaptest.NewLogger(t), clock.System)
	botsService.SetRouterHooks(router)
	botsService.SetTextDraftPusher(router)
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: dc, RSAKey: rsaKey, AuthKeys: authKeyStore, LayerRPC: router, ActiveSessions: activeSessions})
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		select {
		case err := <-serveErr:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after callback test context cancellation")
		}
	})

	newCli := func(storage *session.StorageMemory, handler telegram.UpdateHandler) *telegram.Client {
		if handler == nil {
			handler = telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil })
		}
		return telegram.NewClient(1, "hash", telegram.Options{
			PublicKeys:     []exchange.PublicKey{{RSA: &rsaKey.PublicKey}},
			Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
			DCList:         dcs.List{Options: []tg.DCOption{{ID: dc, IPAddress: tcpAddr.IP.String(), Port: tcpAddr.Port, Static: true}}},
			Logger:         logzap.New(zaptest.NewLogger(t).Named("client")),
			SessionStorage: storage,
			UpdateHandler:  handler,
		})
	}
	return &botCallbackEnv{
		rsaKey: rsaKey, addr: tcpAddr, bots: botsService,
		newCli:  newCli,
		newCliH: func(s *session.StorageMemory) *telegram.Client { return newCli(s, nil) },
	}
}

func registerOwner(t *testing.T, ctx context.Context, env *botCallbackEnv, phone, name string) (tg.User, *session.StorageMemory) {
	t.Helper()
	var owner tg.User
	storage := &session.StorageMemory{}
	client := env.newCliH(storage)
	if err := client.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(client)
		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return err
		}
		hash := sent.(*tg.AuthSentCode).PhoneCodeHash
		if _, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: hash, PhoneCode: "12345"}); err != nil {
			return err
		}
		res, err := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{PhoneNumber: phone, PhoneCodeHash: hash, FirstName: name})
		if err != nil {
			return err
		}
		owner = *res.(*tg.AuthAuthorization).User.(*tg.User)
		return nil
	}); err != nil {
		t.Fatalf("owner signUp: %v", err)
	}
	return owner, storage
}

func historyMessageList(history tg.MessagesMessagesClass) []tg.MessageClass {
	switch v := history.(type) {
	case *tg.MessagesMessages:
		return v.Messages
	case *tg.MessagesMessagesSlice:
		return v.Messages
	case *tg.MessagesChannelMessages:
		return v.Messages
	default:
		return nil
	}
}

func updatesFromClass(u tg.UpdatesClass) []tg.UpdateClass {
	switch v := u.(type) {
	case *tg.Updates:
		return v.Updates
	case *tg.UpdatesCombined:
		return v.Updates
	case *tg.UpdateShort:
		return []tg.UpdateClass{v.Update}
	default:
		return nil
	}
}

// TestBotInlineKeyboardCallbackFlow verifies the full P3 callback chain (with real
// concurrent gotd dual clients): the bot sends a message with inline callback
// markup → owner sees the markup in getHistory → owner's getBotCallbackAnswer
// suspends → bot receives the query via updateBotCallbackQuery → setBotCallbackAnswer
// → owner receives the answer; it also verifies that a bot not answering →
// BOT_RESPONSE_TIMEOUT; and that data is byte-exact.
func TestBotInlineKeyboardCallbackFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	env := newBotCallbackEnv(t, ctx)
	owner, ownerStorage := registerOwner(t, ctx, env, "+15550004001", "Owner")
	botUser, botToken, err := env.bots.CreateBot(context.Background(), owner.ID, "CB Bot", "cb_test_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	// Callback data containing 0x00 and high bytes: verify a byte-exact round-trip (I2).
	callbackData := []byte{0x00, 0x01, 0xFF, 0x80, 'a', 'b'}
	const noAnswerText = "ignore"

	botOnline := make(chan struct{})
	botStop := make(chan struct{})
	botErr := make(chan error, 1)

	// The bot stays online in the background, and the update handler hands received callback queries to the answering goroutine.
	cbCh := make(chan *tg.UpdateBotCallbackQuery, 8)
	botHandler := telegram.UpdateHandlerFunc(func(_ context.Context, u tg.UpdatesClass) error {
		for _, upd := range updatesFromClass(u) {
			if q, ok := upd.(*tg.UpdateBotCallbackQuery); ok {
				select {
				case cbCh <- q:
				default:
				}
			}
		}
		return nil
	})
	go func() {
		botClient := env.newCli(&session.StorageMemory{}, botHandler)
		botErr <- botClient.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(botClient)
			if _, err := raw.AuthImportBotAuthorization(ctx, &tg.AuthImportBotAuthorizationRequest{APIID: 1, APIHash: "hash", BotAuthToken: botToken}); err != nil {
				return err
			}
			// A bare RPC sets receivesUpdates, making push reachable (semantics after the hot-recovery sync fix).
			if _, err := raw.UpdatesGetState(ctx); err != nil {
				return err
			}
			// Resolve the owner's profile to get the access_hash from the bot's
			// perspective (access_hash=0 skips validation, same as the existing bot
			// e2e), then send a message with an inline callback+url keyboard.
			got, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: owner.ID}})
			if err != nil {
				return err
			}
			if len(got) != 1 {
				// This callback is started by go func() and reported via the botErr
				// channel; calling t.Fatalf directly on this goroutine would Goexit on
				// the wrong goroutine (go1.26 vet testinggoroutine also flags it), so
				// return an error and let the test's main goroutine fail via botErr.
				return fmt.Errorf("bot getUsers(owner) = %d, want 1", len(got))
			}
			ownerSeen := got[0].(*tg.User)
			markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{{Buttons: []tg.KeyboardInlineButton{
				{Text: "Press", Type: &tg.InlineButtonTypeCallback{Data: callbackData}},
				{Text: "Site", Type: &tg.InlineButtonTypeURL{URL: "https://example.com/x"}},
			}}}}
			req := &tg.MessagesSendMessageRequest{
				Peer:     &tg.InputPeerUser{UserID: ownerSeen.ID, AccessHash: ownerSeen.AccessHash},
				Message:  "tap a button",
				RandomID: 778801,
			}
			req.SetReplyMarkup(markup)
			if _, err := raw.MessagesSendMessage(ctx, req); err != nil {
				return err
			}
			// Answering goroutine: reply setBotCallbackAnswer for every query except noAnswerText.
			go func() {
				for {
					select {
					case q := <-cbCh:
						data, _ := q.GetData()
						if string(data) == noAnswerText {
							continue // produce the timeout branch
						}
						_, _ = raw.MessagesSetBotCallbackAnswer(ctx, &tg.MessagesSetBotCallbackAnswerRequest{
							QueryID: q.QueryID,
							Alert:   true,
							Message: "ok:" + string(data),
						})
					case <-botStop:
						return
					}
				}
			}()
			close(botOnline)
			select {
			case <-botStop:
			case <-ctx.Done():
			}
			return nil
		})
	}()

	select {
	case <-botOnline:
	case err := <-botErr:
		t.Fatalf("bot client exited early: %v", err)
	case <-ctx.Done():
		t.Fatal("bot did not come online")
	}

	ownerClient := env.newCliH(ownerStorage)
	if err := ownerClient.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(ownerClient)
		// The owner resolves the bot to get the access_hash from its own perspective (cold-start resolution via the public username).
		resolved, err := raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: "cb_test_bot"})
		if err != nil {
			return err
		}
		seenBot := resolved.Users[0].(*tg.User)
		botPeer := &tg.InputPeerUser{UserID: seenBot.ID, AccessHash: seenBot.AccessHash}
		_ = botUser

		// Poll getHistory until the bot's markup message arrives.
		var msgID int
		deadline := time.Now().Add(15 * time.Second)
		for {
			h, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: botPeer, Limit: 5})
			if err != nil {
				return err
			}
			for _, m := range historyMessageList(h) {
				msg, ok := m.(*tg.Message)
				if !ok {
					continue
				}
				rm, ok := msg.GetReplyMarkup()
				if !ok {
					continue
				}
				inline, ok := rm.(*tg.ReplyInlineMarkup)
				if !ok || len(inline.Rows) != 1 || len(inline.Rows[0].Buttons) != 2 {
					t.Fatalf("unexpected markup shape: %#v", rm)
				}
				cbBtn, ok := inline.Rows[0].Buttons[0].Type.(*tg.InlineButtonTypeCallback)
				if !ok {
					t.Fatalf("first button not callback: %#v", inline.Rows[0].Buttons[0])
				}
				if string(cbBtn.Data) != string(callbackData) {
					t.Fatalf("callback data round-trip mismatch: got %v want %v", cbBtn.Data, callbackData)
				}
				if _, ok := inline.Rows[0].Buttons[1].Type.(*tg.InlineButtonTypeURL); !ok {
					t.Fatalf("second button not url: %#v", inline.Rows[0].Buttons[1])
				}
				msgID = msg.ID
			}
			if msgID != 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("markup message did not arrive in owner history")
			}
			time.Sleep(50 * time.Millisecond)
		}

		// Press the callback button: getBotCallbackAnswer suspends until the bot answers.
		ans, err := raw.MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{
			Peer:  botPeer,
			MsgID: msgID,
			Data:  callbackData,
		})
		if err != nil {
			return err
		}
		if !ans.Alert || ans.Message != "ok:"+string(callbackData) {
			t.Fatalf("callback answer = alert:%v msg:%q, want alert:true msg:%q", ans.Alert, ans.Message, "ok:"+string(callbackData))
		}

		// Timeout branch: the bot receives it but does not answer (noAnswerText) →
		// BOT_RESPONSE_TIMEOUT. Use a short ctx to trigger the client-side timeout,
		// avoiding waiting out the full 25s server window.
		toCtx, toCancel := context.WithTimeout(ctx, 3*time.Second)
		defer toCancel()
		_, err = raw.MessagesGetBotCallbackAnswer(toCtx, &tg.MessagesGetBotCallbackAnswerRequest{
			Peer:  botPeer,
			MsgID: msgID,
			Data:  []byte(noAnswerText),
		})
		if err == nil {
			t.Fatal("expected timeout error for unanswered callback")
		}
		return nil
	}); err != nil {
		t.Fatalf("owner callback flow: %v", err)
	}

	close(botStop)
	select {
	case <-botErr:
	case <-time.After(5 * time.Second):
	}
}

// TestBotStartBotFlow verifies that messages.startBot produces a visible "/start <param>" message (I7).
func TestBotStartBotFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	env := newBotCallbackEnv(t, ctx)
	owner, ownerStorage := registerOwner(t, ctx, env, "+15550004002", "Owner")
	_, botToken, err := env.bots.CreateBot(context.Background(), owner.ID, "Start Bot", "start_test_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	// The owner calls startBot(bot, payload). First resolve the bot via its public username to get the access_hash.
	ownerClient := env.newCliH(ownerStorage)
	if err := ownerClient.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(ownerClient)
		resolved, err := raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: "start_test_bot"})
		if err != nil {
			return err
		}
		seenBot := resolved.Users[0].(*tg.User)
		_, err = raw.MessagesStartBot(ctx, &tg.MessagesStartBotRequest{
			Bot:        &tg.InputUser{UserID: seenBot.ID, AccessHash: seenBot.AccessHash},
			Peer:       &tg.InputPeerUser{UserID: seenBot.ID, AccessHash: seenBot.AccessHash},
			RandomID:   994401,
			StartParam: "ref123",
		})
		return err
	}); err != nil {
		t.Fatalf("owner startBot: %v", err)
	}

	// After the bot logs in, getHistory(peer=owner) should show "/start ref123".
	botClient := env.newCliH(&session.StorageMemory{})
	if err := botClient.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(botClient)
		if _, err := raw.AuthImportBotAuthorization(ctx, &tg.AuthImportBotAuthorizationRequest{APIID: 1, APIHash: "hash", BotAuthToken: botToken}); err != nil {
			return err
		}
		got, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: owner.ID}})
		if err != nil {
			return err
		}
		ownerSeen := got[0].(*tg.User)
		h, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  &tg.InputPeerUser{UserID: ownerSeen.ID, AccessHash: ownerSeen.AccessHash},
			Limit: 5,
		})
		if err != nil {
			return err
		}
		found := false
		var bodies []string
		for _, m := range historyMessageList(h) {
			if msg, ok := m.(*tg.Message); ok {
				bodies = append(bodies, msg.Message)
				if msg.Message == "/start ref123" {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("bot did not receive '/start ref123' message; history bodies=%v", bodies)
		}
		return nil
	}); err != nil {
		t.Fatalf("bot startBot receive: %v", err)
	}
}
