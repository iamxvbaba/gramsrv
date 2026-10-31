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

// TestLoginRegisterFlow is an end-to-end check of the login/registration loop:
// a telegram.Client connects to the local server and goes sendCode → signIn
// (registration required) → signUp → getUsers(self), verifying that self can be
// looked up after registration.
func TestLoginRegisterFlow(t *testing.T) {
	const (
		dc        = 2
		phone     = "+8613800138000"
		wantPhone = "8613800138000"
		code      = "12345"
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
	// The seed hash must be higher than the help service's code default hash (a
	// store row below the default is treated as stale seed residue and overwritten
	// by the default config), otherwise the assertion would get the default config.
	const seedAppConfigHash = 1_000_000
	if err := helpStore.UpsertAppConfig(context.Background(), domain.AppConfig{
		Client: "tdesktop",
		Hash:   seedAppConfigHash,
		JSON:   []byte(`{"chat_read_mark_expire_period":604800,"chat_read_mark_size_threshold":50,"pm_read_date_expire_period":604800,"quote_length_max":1024,"telegram_antispam_group_size_min":200,"telegram_antispam_user_id":"5434988373"}`),
	}); err != nil {
		t.Fatalf("seed app config: %v", err)
	}
	if err := helpStore.UpsertCountries(context.Background(), []domain.Country{
		{ISO2: "US", DefaultName: "United States", CountryCodes: []domain.CountryCode{{CountryCode: "1", Prefixes: []string{"1"}}}},
	}); err != nil {
		t.Fatalf("seed countries: %v", err)
	}
	langPackStore := memory.NewLangPackStore()
	if err := langPackStore.UpsertPack(context.Background(), domain.LangPack{
		LangPack: "tdesktop",
		LangCode: "en",
		Version:  1,
		Strings:  []domain.LangPackString{{Key: "lng_language_name", Value: "English"}},
	}); err != nil {
		t.Fatalf("seed langpack: %v", err)
	}
	deps := rpc.Deps{
		Auth:     auth.NewService(userStore, authzStore, memory.NewCodeStore(), authKeyStore, memory.NewTempAuthKeyBindingStore(authKeyStore), code),
		Account:  account.NewService(memory.NewPasswordStore()),
		Help:     help.NewService(helpStore, helpStore),
		Users:    users.NewService(userStore),
		Updates:  updates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
		Contacts: contacts.NewService(memory.NewContactStore()),
		Dialogs:  dialogs.NewService(memory.NewDialogStore()),
		LangPack: langpack.NewService(langPackStore),
	}
	router := rpc.New(rpc.Config{DC: dc, IP: tcpAddr.IP.String(), Port: tcpAddr.Port}, deps, zaptest.NewLogger(t), clock.System)
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: dc, RSAKey: rsaKey, AuthKeys: authKeyStore, LayerRPC: router})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	opts := telegram.Options{
		PublicKeys:     []exchange.PublicKey{{RSA: &rsaKey.PublicKey}},
		Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
		DCList:         dcs.List{Options: []tg.DCOption{{ID: dc, IPAddress: tcpAddr.IP.String(), Port: tcpAddr.Port, Static: true}}},
		Logger:         logzap.New(zaptest.NewLogger(t).Named("client")),
		SessionStorage: &session.StorageMemory{},
		UpdateHandler:  telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil }),
	}
	client := telegram.NewClient(1, "hash", opts)

	if err := client.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(client)

		// 1) sendCode → phone_code_hash
		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{
			PhoneNumber: phone,
			APIID:       1,
			APIHash:     "hash",
			Settings:    tg.CodeSettings{},
		})
		if err != nil {
			return err
		}
		sentCode, ok := sent.(*tg.AuthSentCode)
		if !ok {
			t.Fatalf("sendCode result = %T, want *tg.AuthSentCode", sent)
		}
		hash := sentCode.PhoneCodeHash

		// 2) signIn → a new user should get SignUpRequired
		signInRes, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{
			PhoneNumber:   phone,
			PhoneCodeHash: hash,
			PhoneCode:     code,
		})
		if err != nil {
			return err
		}
		if _, ok := signInRes.(*tg.AuthAuthorizationSignUpRequired); !ok {
			t.Fatalf("signIn result = %T, want *tg.AuthAuthorizationSignUpRequired", signInRes)
		}

		// 3) signUp → create the user and return the authorization
		signUpRes, err := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{
			PhoneNumber:   phone,
			PhoneCodeHash: hash,
			FirstName:     "Test",
			LastName:      "User",
		})
		if err != nil {
			return err
		}
		authz, ok := signUpRes.(*tg.AuthAuthorization)
		if !ok {
			t.Fatalf("signUp result = %T, want *tg.AuthAuthorization", signUpRes)
		}
		newUser, ok := authz.User.(*tg.User)
		if !ok {
			t.Fatalf("signUp user = %T, want *tg.User", authz.User)
		}
		if !newUser.Self || newUser.FirstName != "Test" || newUser.Phone != wantPhone {
			t.Fatalf("signUp user = %+v, want self FirstName=Test Phone=%s", newUser, wantPhone)
		}

		// 4) getUsers(self) → self can be looked up after registration
		got, err := raw.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
		if err != nil {
			return err
		}
		if len(got) != 1 {
			t.Fatalf("getUsers returned %d users, want 1", len(got))
		}
		self, ok := got[0].(*tg.User)
		if !ok {
			t.Fatalf("getUsers[0] = %T, want *tg.User", got[0])
		}
		if self.ID != newUser.ID || self.FirstName != "Test" || self.Phone != wantPhone {
			t.Fatalf("getUsers self = %+v, want id=%d FirstName=Test Phone=%s", self, newUser.ID, wantPhone)
		}

		// 5) Startup config, account security and post-login empty-account RPCs go
		// through the business services and are encodable.
		appConfig, err := raw.HelpGetAppConfig(ctx, 0)
		if err != nil {
			return err
		}
		// Note: inside the client.Run callback, t.Fatalf only kills the current
		// goroutine while the test's main goroutine waits until the ctx times out—so
		// on assertion failure use return fmt.Errorf to make Run return immediately.
		cfg, ok := appConfig.(*tg.HelpAppConfig)
		if !ok || cfg.Hash != seedAppConfigHash {
			return fmt.Errorf("help.getAppConfig = %T %+v, want seeded base hash=%d", appConfig, appConfig, seedAppConfigHash)
		}
		object, ok := cfg.Config.(*tg.JSONObject)
		if !ok {
			return fmt.Errorf("help.getAppConfig config = %T, want *tg.JSONObject", cfg.Config)
		}
		values := make(map[string]tg.JSONValueClass, len(object.Value))
		for _, item := range object.Value {
			values[item.Key] = item.Value
		}
		for _, key := range []string{"freeze_since_date", "freeze_until_date", "freeze_appeal_url"} {
			if _, ok := values[key]; ok {
				return fmt.Errorf("help.getAppConfig unexpectedly included inactive freeze field %s", key)
			}
		}
		if value, ok := values["quote_length_max"].(*tg.JSONNumber); !ok || value.Value != 1024 {
			return fmt.Errorf("help.getAppConfig lost seeded base config: quote_length_max=%T %+v", values["quote_length_max"], values["quote_length_max"])
		}
		countriesRes, err := raw.HelpGetCountriesList(ctx, &tg.HelpGetCountriesListRequest{LangCode: "en"})
		if err != nil {
			return err
		}
		if countries, ok := countriesRes.(*tg.HelpCountriesList); !ok || len(countries.Countries) != 1 {
			t.Fatalf("help.getCountriesList = %T %+v, want 1 country", countriesRes, countriesRes)
		}
		password, err := raw.AccountGetPassword(ctx)
		if err != nil {
			return err
		}
		if password.HasPassword || len(password.SecureRandom) == 0 {
			t.Fatalf("account.getPassword = %+v, want no password with secure random", password)
		}
		state, err := raw.UpdatesGetState(ctx)
		if err != nil {
			return err
		}
		if state.Date == 0 {
			t.Fatal("updates.getState Date is zero")
		}
		diff, err := raw.UpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{
			Pts:  state.Pts,
			Date: state.Date,
			Qts:  state.Qts,
		})
		if err != nil {
			return err
		}
		if _, ok := diff.(*tg.UpdatesDifferenceEmpty); !ok {
			t.Fatalf("updates.getDifference = %T, want *tg.UpdatesDifferenceEmpty", diff)
		}
		contactsRes, err := raw.ContactsGetContacts(ctx, 0)
		if err != nil {
			return err
		}
		if contacts, ok := contactsRes.(*tg.ContactsContacts); !ok || len(contacts.Contacts) != 0 {
			t.Fatalf("contacts.getContacts = %T %+v, want empty *tg.ContactsContacts", contactsRes, contactsRes)
		}
		dialogsRes, err := raw.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetPeer: &tg.InputPeerEmpty{},
			Limit:      20,
		})
		if err != nil {
			return err
		}
		if dialogs, ok := dialogsRes.(*tg.MessagesDialogs); !ok || len(dialogs.Dialogs) != 0 {
			t.Fatalf("messages.getDialogs = %T %+v, want empty *tg.MessagesDialogs", dialogsRes, dialogsRes)
		}
		pinned, err := raw.MessagesGetPinnedDialogs(ctx, 0)
		if err != nil {
			return err
		}
		if len(pinned.Dialogs) != 0 || pinned.State.Date == 0 {
			t.Fatalf("messages.getPinnedDialogs = %+v, want empty dialogs with state", pinned)
		}
		pack, err := raw.LangpackGetLangPack(ctx, &tg.LangpackGetLangPackRequest{
			LangPack: "tdesktop",
			LangCode: "en",
		})
		if err != nil {
			return err
		}
		if pack.Version != 1 || len(pack.Strings) != 1 {
			t.Fatalf("langpack.getLangPack = %+v, want version 1 with 1 string", pack)
		}
		strings, err := raw.LangpackGetStrings(ctx, &tg.LangpackGetStringsRequest{
			LangPack: "tdesktop",
			LangCode: "en",
			Keys:     []string{"lng_language_name"},
		})
		if err != nil {
			return err
		}
		if len(strings) != 1 {
			t.Fatalf("langpack.getStrings returned %d strings, want 1", len(strings))
		}
		return nil
	}); err != nil {
		t.Fatalf("login/register flow: %v", err)
	}

	cancel()
	if err := <-serveErr; err != nil {
		t.Errorf("serve: %v", err)
	}
}

func TestPrivateMessageRoundTripFlow(t *testing.T) {
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
	activeSessions := NewSessionManager(zaptest.NewLogger(t).Named("sessions"))
	deps := rpc.Deps{
		Auth:     auth.NewService(userStore, authzStore, memory.NewCodeStore(), authKeyStore, memory.NewTempAuthKeyBindingStore(authKeyStore), code),
		Account:  account.NewService(memory.NewPasswordStore()),
		Help:     help.NewService(helpStore, helpStore),
		Users:    users.NewService(userStore),
		Updates:  updates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
		Contacts: contacts.NewService(memory.NewContactStore()),
		Dialogs:  dialogs.NewService(dialogStore),
		Messages: messageapp.NewService(messageStore, dialogStore),
		LangPack: langpack.NewService(langPackStore),
		Sessions: activeSessions,
	}
	router := rpc.New(rpc.Config{DC: dc, IP: tcpAddr.IP.String(), Port: tcpAddr.Port}, deps, zaptest.NewLogger(t), clock.System)
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: dc, RSAKey: rsaKey, AuthKeys: authKeyStore, LayerRPC: router, ActiveSessions: activeSessions})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	storageA := &session.StorageMemory{}
	storageB := &session.StorageMemory{}

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

	signUp := func(storage *session.StorageMemory, phone, firstName string) tg.User {
		t.Helper()
		client := newClient(storage)
		var out tg.User
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{
				PhoneNumber: phone,
				APIID:       1,
				APIHash:     "hash",
				Settings:    tg.CodeSettings{},
			})
			if err != nil {
				return err
			}
			hash := sent.(*tg.AuthSentCode).PhoneCodeHash
			if _, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{
				PhoneNumber:   phone,
				PhoneCodeHash: hash,
				PhoneCode:     code,
			}); err != nil {
				return err
			}
			res, err := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{
				PhoneNumber:   phone,
				PhoneCodeHash: hash,
				FirstName:     firstName,
			})
			if err != nil {
				return err
			}
			authz := res.(*tg.AuthAuthorization)
			u := authz.User.(*tg.User)
			out = *u
			return nil
		}); err != nil {
			t.Fatalf("signUp %s: %v", firstName, err)
		}
		return out
	}

	userA := signUp(storageA, "+15550001001", "Alice")
	userB := signUp(storageB, "+15550001002", "Bob")

	sendAndRead := func(storage *session.StorageMemory, to tg.User, body string, randomID int64) {
		t.Helper()
		client := newClient(storage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			updates, err := raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
				Peer:     &tg.InputPeerUser{UserID: to.ID, AccessHash: to.AccessHash},
				Message:  body,
				RandomID: randomID,
			})
			if err != nil {
				return err
			}
			gotUpdates, ok := updates.(*tg.Updates)
			if !ok || len(gotUpdates.Updates) < 2 {
				t.Fatalf("send updates = %T %+v, want message id + new message", updates, updates)
			}
			history, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer:  &tg.InputPeerUser{UserID: to.ID, AccessHash: to.AccessHash},
				Limit: 10,
			})
			if err != nil {
				return err
			}
			msgs := messagesOf(history)
			if len(msgs) == 0 {
				t.Fatalf("history = %T %+v, want messages", history, history)
			}
			msg, ok := msgs[0].(*tg.Message)
			if !ok || msg.Message != body || !msg.Out {
				t.Fatalf("latest history message = %#v, want outgoing %q", msgs[0], body)
			}
			return nil
		}); err != nil {
			t.Fatalf("send %q: %v", body, err)
		}
	}

	sendAndRead(storageA, userB, "hello bob", 1001)

	clientB := newClient(storageB)
	if err := clientB.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(clientB)
		history, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  &tg.InputPeerUser{UserID: userA.ID, AccessHash: userA.AccessHash},
			Limit: 10,
		})
		if err != nil {
			return err
		}
		msgs := messagesOf(history)
		if len(msgs) == 0 {
			t.Fatalf("bob history = %T %+v, want incoming message", history, history)
		}
		msg, ok := msgs[0].(*tg.Message)
		if !ok || msg.Message != "hello bob" || msg.Out {
			t.Fatalf("bob latest message = %#v, want incoming hello bob", msgs[0])
		}
		return nil
	}); err != nil {
		t.Fatalf("bob read incoming: %v", err)
	}

	sendAndRead(storageB, userA, "hi alice", 2001)

	cancel()
	if err := <-serveErr; err != nil {
		t.Errorf("serve: %v", err)
	}
}
