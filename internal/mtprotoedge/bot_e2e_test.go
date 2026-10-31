package mtprotoedge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"regexp"
	"strings"
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
	"github.com/iamxvbaba/td/tgerr"
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
	"telesrv/internal/domain"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

var botTokenRe = regexp.MustCompile(`(\d+):([A-Za-z0-9_-]{35})`)

// TestBotFatherCreateAndBotLoginFlow is the bot main-chain end-to-end verification:
// TestBotManagementRPCFlow verifies the P2 bots.* management RPC end to end:
// after the bot itself calls setBotCommands/setBotInfo/setBotMenuButton, getFullUser
// reflects the new values and bot_info_version increases monotonically; from the
// owner's perspective getFullUser(bot) carries bot_can_edit and the owner can
// modify the bot name via bot:InputUser.
func TestBotManagementRPCFlow(t *testing.T) {
	const (
		dc   = 2
		code = "12345"
	)
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
			memory.NewTempAuthKeyBindingStore(authKeyStore), code, auth.WithBotLogin(botStore)),
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	newClient := func(storage *session.StorageMemory) *telegram.Client {
		opts := telegram.Options{
			PublicKeys:     []exchange.PublicKey{{RSA: &rsaKey.PublicKey}},
			Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
			DCList:         dcs.List{Options: []tg.DCOption{{ID: dc, IPAddress: tcpAddr.IP.String(), Port: tcpAddr.Port, Static: true}}},
			Logger:         logzap.New(zaptest.NewLogger(t).Named("client")),
			SessionStorage: storage,
			UpdateHandler:  telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil }),
		}
		return telegram.NewClient(1, "hash", opts)
	}

	// Owner registers.
	ownerStorage := &session.StorageMemory{}
	var owner tg.User
	{
		client := newClient(ownerStorage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: "+15550003001", APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
			if err != nil {
				return err
			}
			hash := sent.(*tg.AuthSentCode).PhoneCodeHash
			if _, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: "+15550003001", PhoneCodeHash: hash, PhoneCode: code}); err != nil {
				return err
			}
			res, err := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{PhoneNumber: "+15550003001", PhoneCodeHash: hash, FirstName: "Owner"})
			if err != nil {
				return err
			}
			owner = *res.(*tg.AuthAuthorization).User.(*tg.User)
			return nil
		}); err != nil {
			t.Fatalf("owner signUp: %v", err)
		}
	}

	// Create the bot directly via the service (bypassing the BotFather conversation, focusing on the management RPC).
	botUser, botToken, err := botsService.CreateBot(context.Background(), owner.ID, "Mgmt Bot", "mgmt_test_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	getFullSelfBotInfo := func(raw *tg.Client) (tg.BotInfo, *tg.User) {
		t.Helper()
		full, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		if err != nil {
			t.Fatalf("getFullUser self: %v", err)
		}
		bi, ok := full.FullUser.GetBotInfo()
		if !ok {
			t.Fatalf("self userFull lacks bot_info")
		}
		return bi, full.Users[0].(*tg.User)
	}

	// The bot logs in and calls the management RPC.
	botStorage := &session.StorageMemory{}
	var verAfterCommands int
	{
		client := newClient(botStorage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			if _, err := raw.AuthImportBotAuthorization(ctx, &tg.AuthImportBotAuthorizationRequest{APIID: 1, APIHash: "hash", BotAuthToken: botToken}); err != nil {
				return err
			}
			_, self0 := getFullSelfBotInfo(raw)
			v0, _ := self0.GetBotInfoVersion()

			// setBotCommands（default scope）。
			ok, err := raw.BotsSetBotCommands(ctx, &tg.BotsSetBotCommandsRequest{
				Scope:    &tg.BotCommandScopeDefault{},
				LangCode: "",
				Commands: []tg.BotCommand{{Command: "start", Description: "begin"}, {Command: "help", Description: "show help"}},
			})
			if err != nil || !ok {
				t.Fatalf("setBotCommands = %v,%v", ok, err)
			}
			bi, self1 := getFullSelfBotInfo(raw)
			cmds, _ := bi.GetCommands()
			if len(cmds) != 2 || cmds[0].Command != "start" {
				t.Fatalf("bot_info commands = %+v, want [start,help]", cmds)
			}
			v1, _ := self1.GetBotInfoVersion()
			if v1 <= v0 {
				t.Fatalf("bot_info_version not bumped after setBotCommands: %d -> %d", v0, v1)
			}
			verAfterCommands = v1

			// Read back getBotCommands.
			got, err := raw.BotsGetBotCommands(ctx, &tg.BotsGetBotCommandsRequest{Scope: &tg.BotCommandScopeDefault{}})
			if err != nil || len(got) != 2 {
				t.Fatalf("getBotCommands = %+v, %v", got, err)
			}

			// setBotMenuButton(webview)。
			if ok, err := raw.BotsSetBotMenuButton(ctx, &tg.BotsSetBotMenuButtonRequest{
				UserID: &tg.InputUserSelf{},
				Button: &tg.BotMenuButton{Text: "Open", URL: "https://example.com/app"},
			}); err != nil || !ok {
				t.Fatalf("setBotMenuButton = %v,%v", ok, err)
			}
			bi2, _ := getFullSelfBotInfo(raw)
			mb, _ := bi2.GetMenuButton()
			if btn, ok := mb.(*tg.BotMenuButton); !ok || btn.URL != "https://example.com/app" {
				t.Fatalf("menu button = %#v, want webview", mb)
			}

			// setBotInfo(description) by the bot itself (without the bot parameter); Description is flag.1 and must be set via SetDescription.
			infoReq := &tg.BotsSetBotInfoRequest{LangCode: ""}
			infoReq.SetDescription("what I do")
			if ok, err := raw.BotsSetBotInfo(ctx, infoReq); err != nil || !ok {
				t.Fatalf("setBotInfo(description) = %v,%v", ok, err)
			}
			info, err := raw.BotsGetBotInfo(ctx, &tg.BotsGetBotInfoRequest{LangCode: ""})
			if err != nil || info.Description != "what I do" {
				t.Fatalf("getBotInfo = %#v, %v", info, err)
			}

			// The bot itself should not carry bot_can_edit (only the owner's perspective has it).
			_, selfU := getFullSelfBotInfo(raw)
			if selfU.GetBotCanEdit() {
				t.Fatalf("bot self carries bot_can_edit, want only owner view")
			}
			return nil
		}); err != nil {
			t.Fatalf("bot management flow: %v", err)
		}
	}

	// Owner's perspective: getFullUser(bot) carries bot_can_edit + commands; the owner modifies the name.
	{
		client := newClient(ownerStorage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			resolved, err := raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: "mgmt_test_bot"})
			if err != nil {
				return err
			}
			seen := resolved.Users[0].(*tg.User)
			botInput := &tg.InputUser{UserID: seen.ID, AccessHash: seen.AccessHash}
			full, err := raw.UsersGetFullUser(ctx, botInput)
			if err != nil {
				return err
			}
			ownerSeesBot := full.Users[0].(*tg.User)
			if !ownerSeesBot.GetBotCanEdit() {
				t.Fatalf("owner view bot_can_edit = false, want true")
			}
			if bi, ok := full.FullUser.GetBotInfo(); ok {
				if cmds, _ := bi.GetCommands(); len(cmds) != 2 {
					t.Fatalf("owner view bot commands = %d, want 2", len(cmds))
				}
			}
			// The owner modifies the bot name (with bot:InputUser).
			req := &tg.BotsSetBotInfoRequest{LangCode: ""}
			req.SetBot(botInput)
			req.SetName("Owner Renamed")
			if ok, err := raw.BotsSetBotInfo(ctx, req); err != nil || !ok {
				t.Fatalf("owner setBotInfo(name) = %v,%v", ok, err)
			}
			got, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{botInput})
			if err != nil || len(got) != 1 {
				t.Fatalf("getUsers(bot) = %+v, %v", got, err)
			}
			if u := got[0].(*tg.User); u.FirstName != "Owner Renamed" {
				t.Fatalf("bot first_name = %q, want 'Owner Renamed'", u.FirstName)
			}
			return nil
		}); err != nil {
			t.Fatalf("owner management flow: %v", err)
		}
	}

	_ = botUser
	_ = verAfterCommands
	cancel()
	if err := <-serveErr; err != nil {
		t.Errorf("serve: %v", err)
	}
}

// User registers → resolveUsername(BotFather) → /newbot conversation to obtain a
// token → an external client logs in as the bot via auth.importBotAuthorization →
// the user and bot exchange messages → a wrong token gets ACCESS_TOKEN_INVALID.
func TestBotFatherCreateAndBotLoginFlow(t *testing.T) {
	const (
		dc   = 2
		code = "12345"
	)
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
			memory.NewTempAuthKeyBindingStore(authKeyStore), code, auth.WithBotLogin(botStore)),
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	newClient := func(storage *session.StorageMemory) *telegram.Client {
		opts := telegram.Options{
			PublicKeys:     []exchange.PublicKey{{RSA: &rsaKey.PublicKey}},
			Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
			DCList:         dcs.List{Options: []tg.DCOption{{ID: dc, IPAddress: tcpAddr.IP.String(), Port: tcpAddr.Port, Static: true}}},
			Logger:         logzap.New(zaptest.NewLogger(t).Named("client")),
			SessionStorage: storage,
			UpdateHandler:  telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil }),
		}
		return telegram.NewClient(1, "hash", opts)
	}

	messagesOf := func(history tg.MessagesMessagesClass) []tg.MessageClass {
		t.Helper()
		switch v := history.(type) {
		case *tg.MessagesMessages:
			return v.Messages
		case *tg.MessagesMessagesSlice:
			return v.Messages
		default:
			t.Fatalf("history = %T %+v, want messages", history, history)
			return nil
		}
	}

	// 1) Owner registers.
	ownerStorage := &session.StorageMemory{}
	var owner tg.User
	{
		client := newClient(ownerStorage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{
				PhoneNumber: "+15550002001", APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{},
			})
			if err != nil {
				return err
			}
			hash := sent.(*tg.AuthSentCode).PhoneCodeHash
			if _, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{
				PhoneNumber: "+15550002001", PhoneCodeHash: hash, PhoneCode: code,
			}); err != nil {
				return err
			}
			res, err := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{
				PhoneNumber: "+15550002001", PhoneCodeHash: hash, FirstName: "Owner",
			})
			if err != nil {
				return err
			}
			owner = *res.(*tg.AuthAuthorization).User.(*tg.User)
			return nil
		}); err != nil {
			t.Fatalf("owner signUp: %v", err)
		}
	}

	// 2) The owner talks to BotFather to create a bot and obtain a token.
	var (
		botToken    string
		botUsername = "owner_e2e_bot"
		botUserID   int64
	)
	{
		client := newClient(ownerStorage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			resolved, err := raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: "BotFather"})
			if err != nil {
				return err
			}
			if len(resolved.Users) != 1 {
				t.Fatalf("resolve BotFather users = %d, want 1", len(resolved.Users))
			}
			botFather := resolved.Users[0].(*tg.User)
			if botFather.ID != domain.BotFatherUserID || !botFather.Bot {
				t.Fatalf("resolved BotFather = %+v, want bot flag with id %d", botFather, domain.BotFatherUserID)
			}
			if v, ok := botFather.GetBotInfoVersion(); !ok || v < 1 {
				t.Fatalf("BotFather bot_info_version = %d,%v, want >=1", v, ok)
			}
			if _, hasStatus := botFather.GetStatus(); hasStatus {
				t.Fatalf("BotFather carries status %+v, want none for bots", botFather.Status)
			}
			peer := &tg.InputPeerUser{UserID: botFather.ID, AccessHash: botFather.AccessHash}

			// userFull.bot_info must exist and user_id must match (a TDesktop P0 compatibility point).
			full, err := raw.UsersGetFullUser(ctx, &tg.InputUser{UserID: botFather.ID, AccessHash: botFather.AccessHash})
			if err != nil {
				return err
			}
			botInfo, ok := full.FullUser.GetBotInfo()
			if !ok {
				t.Fatalf("BotFather userFull lacks bot_info")
			}
			if id, ok := botInfo.GetUserID(); !ok || id != domain.BotFatherUserID {
				t.Fatalf("bot_info.user_id = %d,%v, want %d", id, ok, domain.BotFatherUserID)
			}
			if cmds, ok := botInfo.GetCommands(); !ok || len(cmds) == 0 {
				t.Fatalf("BotFather bot_info commands empty, want seeded commands")
			}

			randomID := int64(31001)
			// BotFather's reply arrives asynchronously (a goroutine); poll the top of the history until a new incoming reply appears.
			sendAndReply := func(text string) string {
				t.Helper()
				randomID++
				beforeTop := 0
				if h, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 1}); err == nil {
					if m := messagesOf(h); len(m) > 0 {
						if msg, ok := m[0].(*tg.Message); ok {
							beforeTop = msg.ID
						}
					}
				}
				if _, err := raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
					Peer: peer, Message: text, RandomID: randomID,
				}); err != nil {
					t.Fatalf("send %q: %v", text, err)
				}
				deadline := time.Now().Add(10 * time.Second)
				for {
					history, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 5})
					if err != nil {
						t.Fatalf("history after %q: %v", text, err)
					}
					msgs := messagesOf(history)
					if len(msgs) > 0 {
						if top, ok := msgs[0].(*tg.Message); ok && !top.Out && top.ID > beforeTop {
							return top.Message
						}
					}
					if time.Now().After(deadline) {
						t.Fatalf("timed out waiting for BotFather reply to %q", text)
					}
					time.Sleep(50 * time.Millisecond)
				}
			}

			if reply := sendAndReply("/newbot"); !strings.Contains(reply, "choose a name") {
				t.Fatalf("/newbot reply = %q", reply)
			}
			if reply := sendAndReply("Owner E2E Bot"); !strings.Contains(reply, "username") {
				t.Fatalf("name reply = %q", reply)
			}
			reply := sendAndReply(botUsername)
			match := botTokenRe.FindStringSubmatch(reply)
			if match == nil {
				t.Fatalf("done reply = %q, want token", reply)
			}
			botToken = match[0]

			// The new bot can be resolved, and carries the bot flags.
			resolvedBot, err := raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: botUsername})
			if err != nil {
				return err
			}
			created := resolvedBot.Users[0].(*tg.User)
			if !created.Bot {
				t.Fatalf("created bot user lacks bot flag: %+v", created)
			}
			if v, ok := created.GetBotInfoVersion(); !ok || v < 1 {
				t.Fatalf("created bot bot_info_version = %d,%v, want >=1", v, ok)
			}
			botUserID = created.ID
			return nil
		}); err != nil {
			t.Fatalf("newbot flow: %v", err)
		}
	}

	// 3) A wrong token must get ACCESS_TOKEN_INVALID.
	{
		client := newClient(&session.StorageMemory{})
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			_, err := raw.AuthImportBotAuthorization(ctx, &tg.AuthImportBotAuthorizationRequest{
				APIID: 1, APIHash: "hash", BotAuthToken: "12345:notarealtokennotarealtokennotareal_",
			})
			if !tgerr.Is(err, "ACCESS_TOKEN_INVALID") {
				t.Fatalf("bad token err = %v, want ACCESS_TOKEN_INVALID", err)
			}
			return nil
		}); err != nil {
			t.Fatalf("bad token flow: %v", err)
		}
	}

	// 4) The bot client logs in with the token; self must be a bot; userFull.bot_info is self-consistent.
	botStorage := &session.StorageMemory{}
	{
		client := newClient(botStorage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			res, err := raw.AuthImportBotAuthorization(ctx, &tg.AuthImportBotAuthorizationRequest{
				APIID: 1, APIHash: "hash", BotAuthToken: botToken,
			})
			if err != nil {
				return err
			}
			authz, ok := res.(*tg.AuthAuthorization)
			if !ok {
				t.Fatalf("importBotAuthorization = %T, want *tg.AuthAuthorization", res)
			}
			self := authz.User.(*tg.User)
			if self.ID != botUserID || !self.Bot || !self.Self {
				t.Fatalf("bot self = %+v, want self bot id %d", self, botUserID)
			}
			if v, ok := self.GetBotInfoVersion(); !ok || v < 1 {
				t.Fatalf("bot self bot_info_version = %d,%v, want >=1", v, ok)
			}
			// Login takes effect: getUsers(self) and getState work normally.
			got, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
			if err != nil {
				return err
			}
			if len(got) != 1 || got[0].(*tg.User).ID != botUserID {
				t.Fatalf("bot getUsers(self) = %+v, want id %d", got, botUserID)
			}
			if _, err := raw.UpdatesGetState(ctx); err != nil {
				return err
			}
			full, err := raw.UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err != nil {
				return err
			}
			botInfo, ok := full.FullUser.GetBotInfo()
			if !ok {
				t.Fatalf("bot self userFull lacks bot_info")
			}
			if id, ok := botInfo.GetUserID(); !ok || id != botUserID {
				t.Fatalf("bot self bot_info.user_id = %d,%v, want %d", id, ok, botUserID)
			}
			return nil
		}); err != nil {
			t.Fatalf("bot login flow: %v", err)
		}
	}

	// 5) The owner sends a message to the bot.
	{
		client := newClient(ownerStorage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			resolvedBot, err := raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: botUsername})
			if err != nil {
				return err
			}
			created := resolvedBot.Users[0].(*tg.User)
			if _, err := raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
				Peer:     &tg.InputPeerUser{UserID: created.ID, AccessHash: created.AccessHash},
				Message:  "hi bot",
				RandomID: 41001,
			}); err != nil {
				return err
			}
			return nil
		}); err != nil {
			t.Fatalf("owner -> bot send: %v", err)
		}
	}

	// 6) The bot reads the message and replies.
	{
		client := newClient(botStorage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			// access_hash=0 skips validation (consistent with the existing users.getUsers semantics), so the bot obtains the owner's profile this way.
			got, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: owner.ID}})
			if err != nil {
				return err
			}
			if len(got) != 1 {
				t.Fatalf("bot getUsers(owner) = %d users, want 1", len(got))
			}
			ownerSeen := got[0].(*tg.User)
			peer := &tg.InputPeerUser{UserID: ownerSeen.ID, AccessHash: ownerSeen.AccessHash}
			history, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 5})
			if err != nil {
				return err
			}
			msgs := messagesOf(history)
			if len(msgs) == 0 {
				t.Fatalf("bot history empty, want incoming message")
			}
			top, ok := msgs[0].(*tg.Message)
			if !ok || top.Message != "hi bot" || top.Out {
				t.Fatalf("bot latest = %#v, want incoming 'hi bot'", msgs[0])
			}
			if _, err := raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
				Peer:     peer,
				Message:  "hello human",
				RandomID: 51001,
			}); err != nil {
				return err
			}

			// P2: bot self-management (bot-only RPC): setBotCommands / setBotMenuButton.
			ok2, err := raw.BotsSetBotCommands(ctx, &tg.BotsSetBotCommandsRequest{
				Scope:    &tg.BotCommandScopeDefault{},
				LangCode: "",
				Commands: []tg.BotCommand{
					{Command: "start", Description: "start the bot"},
					{Command: "ping", Description: "check liveness"},
				},
			})
			if err != nil || !ok2 {
				t.Fatalf("setBotCommands = %v err=%v, want true", ok2, err)
			}
			cmds, err := raw.BotsGetBotCommands(ctx, &tg.BotsGetBotCommandsRequest{
				Scope: &tg.BotCommandScopeDefault{}, LangCode: "",
			})
			if err != nil || len(cmds) != 2 || cmds[0].Command != "start" {
				t.Fatalf("getBotCommands = %+v err=%v, want [start ping]", cmds, err)
			}
			okBtn, err := raw.BotsSetBotMenuButton(ctx, &tg.BotsSetBotMenuButtonRequest{
				UserID: &tg.InputUserEmpty{},
				Button: &tg.BotMenuButton{Text: "Open", URL: "https://example.test/app"},
			})
			if err != nil || !okBtn {
				t.Fatalf("setBotMenuButton = %v err=%v, want true", okBtn, err)
			}
			gotBtn, err := raw.BotsGetBotMenuButton(ctx, &tg.InputUserEmpty{})
			if err != nil {
				return err
			}
			if web, ok := gotBtn.(*tg.BotMenuButton); !ok || web.URL != "https://example.test/app" {
				t.Fatalf("getBotMenuButton = %#v, want webview button", gotBtn)
			}
			return nil
		}); err != nil {
			t.Fatalf("bot read/reply: %v", err)
		}
	}

	// 7) The owner sees the bot's reply.
	{
		client := newClient(ownerStorage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			resolvedBot, err := raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: botUsername})
			if err != nil {
				return err
			}
			created := resolvedBot.Users[0].(*tg.User)
			history, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer:  &tg.InputPeerUser{UserID: created.ID, AccessHash: created.AccessHash},
				Limit: 5,
			})
			if err != nil {
				return err
			}
			msgs := messagesOf(history)
			if len(msgs) == 0 {
				t.Fatalf("owner history with bot empty")
			}
			top, ok := msgs[0].(*tg.Message)
			if !ok || top.Message != "hello human" || top.Out {
				t.Fatalf("owner latest = %#v, want incoming 'hello human'", msgs[0])
			}

			// P2: the owner's-perspective bot metadata loop.
			// (a) after the bot sets commands + menu button, bot_info_version has bumped (>1).
			if v, ok := created.GetBotInfoVersion(); !ok || v <= 1 {
				t.Fatalf("bot_info_version = %d,%v after metadata changes, want > 1", v, ok)
			}
			// (b) getFullUser: bot_info carries the new commands and the webview menu button; the owner sees bot_can_edit.
			full, err := raw.UsersGetFullUser(ctx, &tg.InputUser{UserID: created.ID, AccessHash: created.AccessHash})
			if err != nil {
				return err
			}
			botInfo, ok := full.FullUser.GetBotInfo()
			if !ok {
				t.Fatal("owner getFullUser(bot) lacks bot_info")
			}
			if cmds, ok := botInfo.GetCommands(); !ok || len(cmds) != 2 || cmds[1].Command != "ping" {
				t.Fatalf("bot_info commands = %+v,%v, want [start ping]", cmds, ok)
			}
			if btn, ok := botInfo.GetMenuButton(); !ok {
				t.Fatal("bot_info lacks menu_button")
			} else if web, isWeb := btn.(*tg.BotMenuButton); !isWeb || web.URL != "https://example.test/app" {
				t.Fatalf("menu_button = %#v, want webview", btn)
			}
			fullUser := full.Users[0].(*tg.User)
			if !fullUser.BotCanEdit {
				t.Fatalf("owner getFullUser(bot) user lacks bot_can_edit: %+v", fullUser)
			}
			// (c) the owner sets setBotInfo (about+description) on the bot's behalf via the bot parameter → getFullUser reflects it.
			setReq := &tg.BotsSetBotInfoRequest{LangCode: ""}
			setReq.SetBot(&tg.InputUser{UserID: created.ID, AccessHash: created.AccessHash})
			setReq.SetAbout("e2e about")
			setReq.SetDescription("e2e description")
			if okSet, err := raw.BotsSetBotInfo(ctx, setReq); err != nil || !okSet {
				t.Fatalf("owner setBotInfo = %v err=%v, want true", okSet, err)
			}
			full2, err := raw.UsersGetFullUser(ctx, &tg.InputUser{UserID: created.ID, AccessHash: created.AccessHash})
			if err != nil {
				return err
			}
			if full2.FullUser.About != "e2e about" {
				t.Fatalf("about after setBotInfo = %q, want 'e2e about'", full2.FullUser.About)
			}
			if bi2, ok := full2.FullUser.GetBotInfo(); !ok {
				t.Fatal("getFullUser after setBotInfo lacks bot_info")
			} else if desc, _ := bi2.GetDescription(); desc != "e2e description" {
				t.Fatalf("description = %q, want 'e2e description'", desc)
			}
			// (d) the owner setting on a bot that is not theirs (BotFather) → BOT_INVALID.
			badReq := &tg.BotsSetBotInfoRequest{LangCode: ""}
			badReq.SetBot(&tg.InputUser{UserID: domain.BotFatherUserID, AccessHash: domain.BotFatherAccessHash})
			badReq.SetAbout("nope")
			if _, err := raw.BotsSetBotInfo(ctx, badReq); !tgerr.Is(err, "BOT_INVALID") {
				t.Fatalf("setBotInfo on BotFather err = %v, want BOT_INVALID", err)
			}
			// (e) a non-bot user calling a bot-only RPC → USER_BOT_REQUIRED.
			if _, err := raw.BotsGetBotCommands(ctx, &tg.BotsGetBotCommandsRequest{
				Scope: &tg.BotCommandScopeDefault{}, LangCode: "",
			}); !tgerr.Is(err, "USER_BOT_REQUIRED") {
				t.Fatalf("owner getBotCommands err = %v, want USER_BOT_REQUIRED", err)
			}
			return nil
		}); err != nil {
			t.Fatalf("owner read bot reply: %v", err)
		}
	}

	// 8) The owner uses BotFather /revoke: the old token becomes invalid + all the
	// bot's authorizations are revoked (the logged-in session invalidation loop; the
	// bot reconnecting with the old auth_key will then get 401).
	{
		if auths, err := authzStore.ListByUser(ctx, botUserID); err != nil || len(auths) == 0 {
			t.Fatalf("bot authorizations before revoke = %d err=%v, want >=1", len(auths), err)
		}
		client := newClient(ownerStorage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			resolved, err := raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: "BotFather"})
			if err != nil {
				return err
			}
			bf := resolved.Users[0].(*tg.User)
			peer := &tg.InputPeerUser{UserID: bf.ID, AccessHash: bf.AccessHash}
			randomID := int64(61001)
			// Wait until BotFather's reply to this entry appears before sending the
			// next—replies are handled asynchronously, and a bare sleep would reorder
			// them under load (see the sleep flaky item in the P2 review). Poll the top
			// for the incoming reply.
			sendAwait := func(text string) {
				t.Helper()
				before := 0
				if h, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 1}); err == nil {
					if m := messagesOf(h); len(m) > 0 {
						if msg, ok := m[0].(*tg.Message); ok {
							before = msg.ID
						}
					}
				}
				randomID++
				if _, err := raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
					Peer: peer, Message: text, RandomID: randomID,
				}); err != nil {
					t.Fatalf("send %q: %v", text, err)
				}
				deadline := time.Now().Add(10 * time.Second)
				for {
					h, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 3})
					if err != nil {
						t.Fatalf("history after %q: %v", text, err)
					}
					msgs := messagesOf(h)
					if len(msgs) > 0 {
						if top, ok := msgs[0].(*tg.Message); ok && !top.Out && top.ID > before {
							return
						}
					}
					if time.Now().After(deadline) {
						t.Fatalf("timed out waiting for BotFather reply to %q", text)
					}
					time.Sleep(40 * time.Millisecond)
				}
			}
			sendAwait("/revoke")
			sendAwait("@" + botUsername)
			// revoke runs inside the BotFather goroutine; poll until the authorization is cleared.
			deadline := time.Now().Add(10 * time.Second)
			for {
				auths, err := authzStore.ListByUser(ctx, botUserID)
				if err != nil {
					return err
				}
				if len(auths) == 0 {
					return nil
				}
				if time.Now().After(deadline) {
					t.Fatalf("bot authorizations not revoked: %d rows remain", len(auths))
				}
				time.Sleep(50 * time.Millisecond)
			}
		}); err != nil {
			t.Fatalf("revoke flow: %v", err)
		}
	}

	cancel()
	if err := <-serveErr; err != nil {
		t.Errorf("serve: %v", err)
	}
}
