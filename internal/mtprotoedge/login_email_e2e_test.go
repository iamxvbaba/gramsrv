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
	"telesrv/internal/app/updates"
	"telesrv/internal/app/users"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

type loginEmailTestSender struct {
	to   string
	code string
}

func (s *loginEmailTestSender) Deliver(_ context.Context, req otpdelivery.Request) (otpdelivery.Result, error) {
	s.to = req.Recipient
	s.code = req.Code
	return otpdelivery.Result{}, nil
}

// TestLoginEmailEndToEnd verifies login email end to end: device A registers and
// sets a login email (loginChange), then a brand-new device B calls sendCode and
// receives sentCodeTypeEmailCode, and completes login with the real email code
// via signIn (email_verification).
func TestLoginEmailEndToEnd(t *testing.T) {
	const (
		dc        = 2
		phone     = "+8613800138777"
		wantPhone = "8613800138777"
		devCode   = "12345"
		email     = "owner@example.com"
		wantMask  = "o***r@example.com"
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
	passwordStore := memory.NewPasswordStore()
	helpStore := memory.NewHelpStore()
	codeStore := memory.NewCodeStore()
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	updateEventStore := memory.NewUpdateEventStore()
	emailSender := &loginEmailTestSender{}
	accountService := account.NewService(passwordStore,
		account.WithUsers(userStore),
		account.WithLoginEmailVerification(codeStore, emailSender, 5*time.Minute, 5, 6))
	authService := auth.NewService(userStore, authzStore, codeStore, authKeyStore, memory.NewTempAuthKeyBindingStore(authKeyStore), devCode,
		auth.WithLoginMessages(messageStore, dialogStore),
		auth.WithLoginCodeDelivery(memory.NewLoginCodeDeliveryStore(messageStore, updateEventStore)),
		auth.WithPhoneCodeDelivery(emailSender, 5),
		auth.WithPasswords(passwordStore),
		auth.WithLoginEmail(auth.LoginEmailOptions{
			Enabled:    true,
			CodeLength: 6,
			Store:      accountService,
			Sender:     emailSender,
		}))

	deps := rpc.Deps{
		Auth:    authService,
		Account: accountService,
		Help:    help.NewService(helpStore, helpStore),
		Users:   users.NewService(userStore),
		Updates: updates.NewService(memory.NewUpdateStateStore(), updateEventStore),

		Contacts: contacts.NewService(memory.NewContactStore()),
		Dialogs:  dialogs.NewService(dialogStore),
	}
	router := rpc.New(rpc.Config{DC: dc, IP: tcpAddr.IP.String(), Port: tcpAddr.Port}, deps, zaptest.NewLogger(t), clock.System)
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: dc, RSAKey: rsaKey, AuthKeys: authKeyStore, LayerRPC: router})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()
	// Shutdown order: after cancel, wait for Serve to actually return (including its
	// trailing logs) before letting the test finish, avoiding the server goroutine
	// writing to the zaptest logger after the test ends and triggering a panic.
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
			SessionStorage: &session.StorageMemory{}, // each client has its own session = its own auth key = its own "device"
			UpdateHandler:  telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil }),
		})
	}

	// Device A: register and set the login email. Inside the client.Run callback,
	// on assertion failure always return error (the callback runs on a separate
	// goroutine, where t.Fatalf only kills that goroutine and makes the test wait
	// idle until timeout).
	deviceA := newClient()
	if err := deviceA.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(deviceA)

		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return err
		}
		hash := sent.(*tg.AuthSentCode).PhoneCodeHash
		if _, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: hash, PhoneCode: emailSender.code}); err != nil {
			return err
		}
		if _, err := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{PhoneNumber: phone, PhoneCodeHash: hash, FirstName: "Owner"}); err != nil {
			return err
		}

		// Set the login email (loginChange, already logged in).
		sentEmail, err := raw.AccountSendVerifyEmailCode(ctx, &tg.AccountSendVerifyEmailCodeRequest{
			Purpose: &tg.EmailVerifyPurposeLoginChange{},
			Email:   email,
		})
		if err != nil {
			return err
		}
		if sentEmail.EmailPattern != wantMask {
			return fmt.Errorf("sentEmailCode pattern = %q, want %q", sentEmail.EmailPattern, wantMask)
		}
		verified, err := raw.AccountVerifyEmail(ctx, &tg.AccountVerifyEmailRequest{
			Purpose:      &tg.EmailVerifyPurposeLoginChange{},
			Verification: &tg.EmailVerificationCode{Code: emailSender.code},
		})
		if err != nil {
			return err
		}
		ev, ok := verified.(*tg.AccountEmailVerified)
		if !ok {
			return fmt.Errorf("verifyEmail result = %T, want *tg.AccountEmailVerified", verified)
		}
		if ev.Email != email {
			return fmt.Errorf("verifyEmail email = %q, want %q", ev.Email, email)
		}

		// getPassword delivers the login email mask.
		pwd, err := raw.AccountGetPassword(ctx)
		if err != nil {
			return err
		}
		gotMask, ok := pwd.GetLoginEmailPattern()
		if !ok || gotMask != wantMask {
			return fmt.Errorf("getPassword login_email_pattern = %q ok=%v, want %q", gotMask, ok, wantMask)
		}
		return nil
	}); err != nil {
		t.Fatalf("device A: %v", err)
	}

	// Device B (brand-new auth key): sendCode should switch to email delivery, and
	// login with any email code.
	deviceB := newClient()
	if err := deviceB.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(deviceB)

		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return err
		}
		sentCode, ok := sent.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("sendCode result = %T, want *tg.AuthSentCode", sent)
		}
		emailType, ok := sentCode.Type.(*tg.AuthSentCodeTypeEmailCode)
		if !ok {
			return fmt.Errorf("sendCode type = %T, want *tg.AuthSentCodeTypeEmailCode (login email should switch delivery)", sentCode.Type)
		}
		if emailType.EmailPattern != wantMask {
			return fmt.Errorf("sentCodeTypeEmailCode pattern = %q, want %q", emailType.EmailPattern, wantMask)
		}

		signInRes, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{
			PhoneNumber:       phone,
			PhoneCodeHash:     sentCode.PhoneCodeHash,
			EmailVerification: &tg.EmailVerificationCode{Code: emailSender.code},
		})
		if err != nil {
			return err
		}
		authz, ok := signInRes.(*tg.AuthAuthorization)
		if !ok {
			return fmt.Errorf("signIn result = %T, want *tg.AuthAuthorization", signInRes)
		}
		self, ok := authz.User.(*tg.User)
		if !ok || !self.Self || self.Phone != wantPhone {
			return fmt.Errorf("signIn user = %+v, want self phone=%s", authz.User, wantPhone)
		}
		return nil
	}); err != nil {
		t.Fatalf("device B: %v", err)
	}

	// Device C: cannot access the login email → resetLoginEmail clears the login
	// email and switches back to phone-code login.
	deviceC := newClient()
	if err := deviceC.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(deviceC)

		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return err
		}
		sentCode := sent.(*tg.AuthSentCode)
		emailType, ok := sentCode.Type.(*tg.AuthSentCodeTypeEmailCode)
		if !ok {
			return fmt.Errorf("pre-reset sendCode type = %T, want email code", sentCode.Type)
		}
		if _, ok := emailType.GetResetAvailablePeriod(); !ok {
			return fmt.Errorf("pre-reset email code omitted reset_available_period with real SMS sender")
		}

		// Reset the login email: the real SMS sender issues a random phone code and
		// returns sentCodeTypeSms.
		resetRes, err := raw.AuthResetLoginEmail(ctx, &tg.AuthResetLoginEmailRequest{PhoneNumber: phone, PhoneCodeHash: sentCode.PhoneCodeHash})
		if err != nil {
			return err
		}
		resetSent, ok := resetRes.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("resetLoginEmail result = %T, want *tg.AuthSentCode", resetRes)
		}
		if _, ok := resetSent.Type.(*tg.AuthSentCodeTypeSMS); !ok {
			return fmt.Errorf("resetLoginEmail sentCode type = %T, want *tg.AuthSentCodeTypeSMS (real phone fallback)", resetSent.Type)
		}

		// Complete login with the dynamic phone code captured by the sender; falling
		// back to the fixed development code is forbidden.
		signInRes, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: resetSent.PhoneCodeHash, PhoneCode: emailSender.code})
		if err != nil {
			return err
		}
		if _, ok := signInRes.(*tg.AuthAuthorization); !ok {
			return fmt.Errorf("post-reset signIn result = %T, want *tg.AuthAuthorization", signInRes)
		}
		return nil
	}); err != nil {
		t.Fatalf("device C: %v", err)
	}
}
