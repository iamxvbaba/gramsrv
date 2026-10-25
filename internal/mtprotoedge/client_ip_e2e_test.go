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
	"telesrv/internal/app/updates"
	"telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

// TestClientIPPersistsToAuthorization verifies the real connection-to-persistence
// path: the accepted connection's remote IP is carried as neutral transport
// metadata by mtprotoedge, flows through RPC routing, and is persisted into the
// device authorization (authorizations.ip). It runs a full login over a real
// MTProto connection and asserts the stored authorization carries the client's
// loopback IP.
func TestClientIPPersistsToAuthorization(t *testing.T) {
	const (
		dc       = 2
		phone    = "+8613800138100"
		code     = "12345"
		clientIP = "127.0.0.1"
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
	if tcpAddr.IP.String() != clientIP {
		t.Fatalf("test listener bound to %s, want loopback %s", tcpAddr.IP, clientIP)
	}

	userStore := memory.NewUserStore()
	authzStore := memory.NewAuthorizationStore()
	deliveryOutbox := memory.NewDeliveryOutboxStore()
	authzStore.AttachDeliveryOutbox(deliveryOutbox)
	authKeyStore := memory.NewAuthKeyStore()
	helpStore := memory.NewHelpStore()
	if err := helpStore.UpsertAppConfig(context.Background(), domain.AppConfig{
		Client: "tdesktop", Hash: 1_000_000,
		JSON: []byte(`{"chat_read_mark_expire_period":604800,"chat_read_mark_size_threshold":50,"pm_read_date_expire_period":604800,"quote_length_max":1024,"telegram_antispam_group_size_min":200,"telegram_antispam_user_id":"5434988373"}`),
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
		LangPack: "tdesktop", LangCode: "en", Version: 1,
		Strings: []domain.LangPackString{{Key: "lng_language_name", Value: "English"}},
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

		DeliveryOutbox:       deliveryOutbox,
		AuthKeySessionLayers: authKeyStore,
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

	var newUserID int64
	if err := client.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(client)

		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return err
		}
		sentCode, ok := sent.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("sendCode result = %T, want *tg.AuthSentCode", sent)
		}

		if _, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: sentCode.PhoneCodeHash, PhoneCode: code}); err != nil {
			return err
		}

		signUpRes, err := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{PhoneNumber: phone, PhoneCodeHash: sentCode.PhoneCodeHash, FirstName: "IP", LastName: "Test"})
		if err != nil {
			return err
		}
		authz, ok := signUpRes.(*tg.AuthAuthorization)
		if !ok {
			return fmt.Errorf("signUp result = %T, want *tg.AuthAuthorization", signUpRes)
		}
		newUser, ok := authz.User.(*tg.User)
		if !ok {
			return fmt.Errorf("signUp user = %T, want *tg.User", authz.User)
		}
		newUserID = newUser.ID
		return nil
	}); err != nil {
		t.Fatalf("client login flow: %v", err)
	}

	auths, err := authzStore.ListByUser(ctx, newUserID)
	if err != nil || len(auths) == 0 {
		t.Fatalf("authorizations for user %d = %d (err=%v), want >=1", newUserID, len(auths), err)
	}
	var gotIP string
	for _, a := range auths {
		if a.IP != "" {
			gotIP = a.IP
			break
		}
	}
	if gotIP != clientIP {
		t.Fatalf("persisted authorization IP = %q, want %q", gotIP, clientIP)
	}

	select {
	case err := <-serveErr:
		t.Fatalf("server stopped unexpectedly: %v", err)
	default:
	}
}
