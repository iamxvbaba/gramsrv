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
	"github.com/gotd/td/clock"
	"github.com/gotd/td/exchange"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/transport"

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

// TestBotFatherCreateAndBotLoginFlow 是 bot 主链路端到端验证：
// TestBotManagementRPCFlow 验证 P2 bots.* 管理 RPC 端到端：
// bot 自己调 setBotCommands/setBotInfo/setBotMenuButton 后 getFullUser 反映新值且
// bot_info_version 单调递增；owner 视角 getFullUser(bot) 带 bot_can_edit 且 owner 可
// 经 bot:InputUser 代改 bot name。
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
			memory.NewTempAuthKeyBindingStore(), code, auth.WithBotLogin(botStore)),
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
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: dc, RSAKey: rsaKey, AuthKeys: authKeyStore, RPC: router, ActiveSessions: activeSessions})

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

	// owner 注册。
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

	// 直接经 service 建 bot（绕过 BotFather 对话，聚焦管理 RPC）。
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

	// bot 登录并调管理 RPC。
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

			// getBotCommands 回读。
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

			// setBotInfo(description) by bot self（不带 bot 参数）；Description 是 flag.1，须 SetDescription 置位。
			infoReq := &tg.BotsSetBotInfoRequest{LangCode: ""}
			infoReq.SetDescription("what I do")
			if ok, err := raw.BotsSetBotInfo(ctx, infoReq); err != nil || !ok {
				t.Fatalf("setBotInfo(description) = %v,%v", ok, err)
			}
			info, err := raw.BotsGetBotInfo(ctx, &tg.BotsGetBotInfoRequest{LangCode: ""})
			if err != nil || info.Description != "what I do" {
				t.Fatalf("getBotInfo = %#v, %v", info, err)
			}

			// bot self 不应带 bot_can_edit（owner 视角才有）。
			_, selfU := getFullSelfBotInfo(raw)
			if selfU.GetBotCanEdit() {
				t.Fatalf("bot self carries bot_can_edit, want only owner view")
			}
			return nil
		}); err != nil {
			t.Fatalf("bot management flow: %v", err)
		}
	}

	// owner 视角：getFullUser(bot) 带 bot_can_edit + commands；owner 代改 name。
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
			// owner 代改 bot name（带 bot:InputUser）。
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

// 用户注册 → resolveUsername(BotFather) → /newbot 对话拿 token →
// 外部客户端 auth.importBotAuthorization 登录为 bot →
// 用户与 bot 互发消息 → 错误 token 拿 ACCESS_TOKEN_INVALID。
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
			memory.NewTempAuthKeyBindingStore(), code, auth.WithBotLogin(botStore)),
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
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: dc, RSAKey: rsaKey, AuthKeys: authKeyStore, RPC: router, ActiveSessions: activeSessions})

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

	// 1) owner 注册。
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

	// 2) owner 与 BotFather 对话创建 bot，拿 token。
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

			// userFull.bot_info 必须存在且 user_id 匹配（TDesktop P0 兼容点）。
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
			// BotFather 回复异步到达（go routine），轮询历史顶部直到出现新的 incoming 回复。
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

			// 新 bot 可被 resolve，且带 bot flags。
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

	// 3) 错误 token 必须拿 ACCESS_TOKEN_INVALID。
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

	// 4) bot 客户端凭 token 登录；self 必须是 bot；userFull.bot_info 自洽。
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
			// 登录态生效：getUsers(self) 与 getState 正常。
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

	// 5) owner 给 bot 发消息。
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

	// 6) bot 读到消息并回复。
	{
		client := newClient(botStorage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			// access_hash=0 跳过校验（与现有 users.getUsers 语义一致），bot 据此拿 owner 资料。
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

			// P2: bot 自管理（bot-only RPC）：setBotCommands / setBotMenuButton。
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

	// 7) owner 看到 bot 回复。
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

			// P2: owner 视角的 bot 元数据闭环。
			// (a) bot 设置命令+菜单按钮后 bot_info_version 已 bump（>1）。
			if v, ok := created.GetBotInfoVersion(); !ok || v <= 1 {
				t.Fatalf("bot_info_version = %d,%v after metadata changes, want > 1", v, ok)
			}
			// (b) getFullUser：bot_info 带新命令与 webview 菜单按钮；owner 看到 bot_can_edit。
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
			// (c) owner 经 bot 参数代设 setBotInfo（about+description）→ getFullUser 反映。
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
			// (d) owner 对非自己的 bot（BotFather）代设 → BOT_INVALID。
			badReq := &tg.BotsSetBotInfoRequest{LangCode: ""}
			badReq.SetBot(&tg.InputUser{UserID: domain.BotFatherUserID, AccessHash: domain.BotFatherAccessHash})
			badReq.SetAbout("nope")
			if _, err := raw.BotsSetBotInfo(ctx, badReq); !tgerr.Is(err, "BOT_INVALID") {
				t.Fatalf("setBotInfo on BotFather err = %v, want BOT_INVALID", err)
			}
			// (e) 非 bot 用户调 bot-only RPC → USER_BOT_REQUIRED。
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

	// 8) owner 走 BotFather /revoke：旧 token 失效 + bot 全部 authorization 被撤销
	//（已登录 session 失效闭环；bot 凭旧 auth_key 重连后将得 401）。
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
			// 等到 BotFather 对该条的回复出现再发下一条——回复异步处理，裸 sleep 在
			// 负载下会乱序（见 P2 审查的 sleep flaky 项）。轮询顶部 incoming 回复。
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
			// revoke 在 BotFather goroutine 内执行；轮询 authorization 清空。
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
