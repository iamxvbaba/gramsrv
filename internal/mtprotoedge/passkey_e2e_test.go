package mtprotoedge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
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
	"github.com/gotd/td/transport"

	"telesrv/internal/app/account"
	"telesrv/internal/app/auth"
	"telesrv/internal/app/contacts"
	"telesrv/internal/app/dialogs"
	"telesrv/internal/app/help"
	passkeyapp "telesrv/internal/app/passkey"
	"telesrv/internal/app/updates"
	"telesrv/internal/app/users"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
	"telesrv/internal/webauthn/webauthntest"
)

const passkeyTestOrigin = "android:apk-key-hash:e2e-test"

// publicKeyOptions 提取 DataJSON(顶层 publicKey)里的 challenge / rpId / user.id。
type publicKeyOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RPID      string `json:"rpId"`
		RP        struct {
			ID string `json:"id"`
		} `json:"rp"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"publicKey"`
}

func parsePasskeyOptions(t *testing.T, data string) publicKeyOptions {
	t.Helper()
	var opts publicKeyOptions
	if err := json.Unmarshal([]byte(data), &opts); err != nil {
		t.Fatalf("parse passkey options %q: %v", data, err)
	}
	return opts
}

func decodeB64URL(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		if b, err = base64.URLEncoding.DecodeString(s); err != nil {
			t.Fatalf("base64url decode %q: %v", s, err)
		}
	}
	return b
}

// TestPasskeyEndToEnd 端到端验证 passkey:设备 A(已登录)注册 passkey,设备 B(全新
// auth key)经 initPasskeyLogin/finishPasskeyLogin 用软件 authenticator 的断言登录;再
// getPasskeys/deletePasskey 往返。整条链路验签真实(软件 authenticator 用真私钥签名)。
func TestPasskeyEndToEnd(t *testing.T) {
	const (
		dc        = 2
		phone     = "+8613800139000"
		wantPhone = "8613800139000"
		code      = "12345"
		rpID      = "telesrv.test"
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
	authKeyStore := memory.NewAuthKeyStore()
	helpStore := memory.NewHelpStore()
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	updateEventStore := memory.NewUpdateEventStore()
	passkeyService := passkeyapp.NewService(memory.NewPasskeyStore(), memory.NewPasskeyChallengeStore(), rpID, dc)

	deps := rpc.Deps{
		Auth: auth.NewService(userStore, memory.NewAuthorizationStore(), memory.NewCodeStore(), authKeyStore, memory.NewTempAuthKeyBindingStore(authKeyStore), code,
			auth.WithLoginMessages(messageStore, dialogStore),
			auth.WithLoginCodeDelivery(memory.NewLoginCodeDeliveryStore(messageStore, updateEventStore))),
		Account: account.NewService(memory.NewPasswordStore(), account.WithUsers(userStore)),
		Help:    help.NewService(helpStore, helpStore),
		Users:   users.NewService(userStore),
		Updates: updates.NewService(memory.NewUpdateStateStore(), updateEventStore),

		Contacts: contacts.NewService(memory.NewContactStore()),
		Dialogs:  dialogs.NewService(dialogStore),
		Passkey:  passkeyService,
	}
	router := rpc.New(rpc.Config{DC: dc, IP: tcpAddr.IP.String(), Port: tcpAddr.Port}, deps, zaptest.NewLogger(t), clock.System)
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: dc, RSAKey: rsaKey, AuthKeys: authKeyStore, RPC: router})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()
	defer func() {
		cancel()
		<-serveErr
	}()

	newClient := func() *telegram.Client {
		return telegram.NewClient(1, "hash", telegram.Options{
			PublicKeys:     []exchange.PublicKey{{RSA: &rsaKey.PublicKey}},
			Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
			DCList:         dcs.List{Options: []tg.DCOption{{ID: dc, IPAddress: tcpAddr.IP.String(), Port: tcpAddr.Port, Static: true}}},
			Logger:         logzap.New(zaptest.NewLogger(t).Named("client")),
			SessionStorage: &session.StorageMemory{},
			UpdateHandler:  telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil }),
		})
	}

	authn, err := webauthntest.New()
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	var userHandle string

	// 设备 A:注册 + 注册 passkey + getPasskeys。
	deviceA := newClient()
	if err := deviceA.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(deviceA)
		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return err
		}
		hash := sent.(*tg.AuthSentCode).PhoneCodeHash
		if _, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: hash, PhoneCode: code}); err != nil {
			return err
		}
		if _, err := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{PhoneNumber: phone, PhoneCodeHash: hash, FirstName: "Pass"}); err != nil {
			return err
		}

		// 1) 注册选项 → 软件 authenticator 造 attestation。
		regOpts, err := raw.AccountInitPasskeyRegistration(ctx)
		if err != nil {
			return err
		}
		opts := parsePasskeyOptions(t, regOpts.Options.Data)
		if opts.PublicKey.RP.ID != rpID {
			return fmt.Errorf("registration rp.id = %q, want %q", opts.PublicKey.RP.ID, rpID)
		}
		userHandle = string(decodeB64URL(t, opts.PublicKey.User.ID)) // "2:<userId>"
		challenge := decodeB64URL(t, opts.PublicKey.Challenge)
		clientData, attObj, err := authn.Register(rpID, passkeyTestOrigin, challenge)
		if err != nil {
			return err
		}
		credB64 := authn.CredentialIDB64()
		cred := &tg.InputPasskeyCredentialPublicKey{
			ID:    credB64,
			RawID: credB64,
			Response: &tg.InputPasskeyResponseRegister{
				ClientData:      tg.DataJSON{Data: string(clientData)},
				AttestationData: attObj,
			},
		}
		pk, err := raw.AccountRegisterPasskey(ctx, cred)
		if err != nil {
			return err
		}
		if pk.ID != credB64 {
			return fmt.Errorf("registered passkey id = %q, want %q", pk.ID, credB64)
		}

		// 2) getPasskeys 应含该 passkey。
		list, err := raw.AccountGetPasskeys(ctx)
		if err != nil {
			return err
		}
		if len(list.Passkeys) != 1 || list.Passkeys[0].ID != credB64 {
			return fmt.Errorf("getPasskeys = %+v, want 1 passkey id=%q", list.Passkeys, credB64)
		}
		return nil
	}); err != nil {
		t.Fatalf("device A: %v", err)
	}

	// 设备 B:全新 auth key,用 passkey 登录。
	deviceB := newClient()
	if err := deviceB.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(deviceB)
		loginOpts, err := raw.AuthInitPasskeyLogin(ctx, &tg.AuthInitPasskeyLoginRequest{APIID: 1, APIHash: "hash"})
		if err != nil {
			return err
		}
		opts := parsePasskeyOptions(t, loginOpts.Options.Data)
		if opts.PublicKey.RPID != rpID {
			return fmt.Errorf("login rpId = %q, want %q", opts.PublicKey.RPID, rpID)
		}
		challenge := decodeB64URL(t, opts.PublicKey.Challenge)
		clientData, authData, sig, err := authn.Assert(rpID, passkeyTestOrigin, challenge)
		if err != nil {
			return err
		}
		credB64 := authn.CredentialIDB64()
		res, err := raw.AuthFinishPasskeyLogin(ctx, &tg.AuthFinishPasskeyLoginRequest{
			Credential: &tg.InputPasskeyCredentialPublicKey{
				ID:    credB64,
				RawID: credB64,
				Response: &tg.InputPasskeyResponseLogin{
					ClientData:        tg.DataJSON{Data: string(clientData)},
					AuthenticatorData: authData,
					Signature:         sig,
					UserHandle:        userHandle,
				},
			},
		})
		if err != nil {
			return err
		}
		authz, ok := res.(*tg.AuthAuthorization)
		if !ok {
			return fmt.Errorf("finishPasskeyLogin result = %T, want *tg.AuthAuthorization", res)
		}
		self, ok := authz.User.(*tg.User)
		if !ok || !self.Self || self.Phone != wantPhone {
			return fmt.Errorf("passkey login user = %+v, want self phone=%s", authz.User, wantPhone)
		}

		// 第二次断言(计数器递增)仍应通过。
		loginOpts2, err := raw.AuthInitPasskeyLogin(ctx, &tg.AuthInitPasskeyLoginRequest{APIID: 1, APIHash: "hash"})
		if err != nil {
			return err
		}
		ch2 := decodeB64URL(t, parsePasskeyOptions(t, loginOpts2.Options.Data).PublicKey.Challenge)
		cd2, ad2, sig2, err := authn.Assert(rpID, passkeyTestOrigin, ch2)
		if err != nil {
			return err
		}
		if _, err := raw.AuthFinishPasskeyLogin(ctx, &tg.AuthFinishPasskeyLoginRequest{
			Credential: &tg.InputPasskeyCredentialPublicKey{
				ID: credB64, RawID: credB64,
				Response: &tg.InputPasskeyResponseLogin{
					ClientData: tg.DataJSON{Data: string(cd2)}, AuthenticatorData: ad2, Signature: sig2, UserHandle: userHandle,
				},
			},
		}); err != nil {
			return fmt.Errorf("second passkey login: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("device B: %v", err)
	}

	// 设备 A 再次连接:删除 passkey → getPasskeys 空。
	deviceC := newClient()
	if err := deviceC.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(deviceC)
		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return err
		}
		if _, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: sent.(*tg.AuthSentCode).PhoneCodeHash, PhoneCode: code}); err != nil {
			return err
		}
		deleted, err := raw.AccountDeletePasskey(ctx, authn.CredentialIDB64())
		if err != nil {
			return err
		}
		if !deleted {
			return fmt.Errorf("deletePasskey returned false")
		}
		list, err := raw.AccountGetPasskeys(ctx)
		if err != nil {
			return err
		}
		if len(list.Passkeys) != 0 {
			return fmt.Errorf("getPasskeys after delete = %+v, want empty", list.Passkeys)
		}
		return nil
	}); err != nil {
		t.Fatalf("device C: %v", err)
	}
}
