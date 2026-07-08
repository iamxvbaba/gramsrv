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
	"telesrv/internal/app/updates"
	"telesrv/internal/app/users"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

type loginEmailTestSender struct {
	to   string
	code string
}

func (s *loginEmailTestSender) SendLoginCode(_ context.Context, to, code string, _ time.Duration) error {
	s.to = to
	s.code = code
	return nil
}

// TestLoginEmailEndToEnd 端到端验证登录邮箱：设备 A 注册并设置登录邮箱（loginChange），
// 一个全新设备 B 调 sendCode 收到 sentCodeTypeEmailCode，凭真实邮箱验证码经 signIn
// (email_verification) 完成登录。
func TestLoginEmailEndToEnd(t *testing.T) {
	const (
		dc        = 2
		phone     = "+8613800138777"
		wantPhone = "8613800138777"
		code      = "12345"
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
	emailSender := &loginEmailTestSender{}
	accountService := account.NewService(passwordStore,
		account.WithUsers(userStore),
		account.WithLoginEmailVerification(codeStore, emailSender, 5*time.Minute, 5, 6))
	authService := auth.NewService(userStore, authzStore, codeStore, authKeyStore, memory.NewTempAuthKeyBindingStore(), code,
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
		Updates: updates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),

		Contacts: contacts.NewService(memory.NewContactStore()),
		Dialogs:  dialogs.NewService(memory.NewDialogStore()),
	}
	router := rpc.New(rpc.Config{DC: dc, IP: tcpAddr.IP.String(), Port: tcpAddr.Port}, deps, zaptest.NewLogger(t), clock.System)
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: dc, RSAKey: rsaKey, AuthKeys: authKeyStore, RPC: router})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()
	// 关停顺序：cancel 后等 Serve 真正返回（含其末尾日志）再让测试结束，避免
	// 服务端 goroutine 在测试完成后写 zaptest logger 触发 panic。
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
			SessionStorage: &session.StorageMemory{}, // 每个 client 独立 session = 独立 auth key = 独立"设备"
			UpdateHandler:  telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil }),
		})
	}

	// 设备 A：注册并设置登录邮箱。client.Run 回调里断言失败一律 return error（回调跑在
	// 独立 goroutine，t.Fatalf 只会杀该 goroutine 并让测试空等到超时）。
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
		if _, err := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{PhoneNumber: phone, PhoneCodeHash: hash, FirstName: "Owner"}); err != nil {
			return err
		}

		// 设置登录邮箱（loginChange，已登录）。
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

		// getPassword 下发登录邮箱掩码。
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

	// 设备 B（全新 auth key）：sendCode 应改投邮箱，凭任意邮箱码登录。
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

	// 设备 C：无法访问登录邮箱 → resetLoginEmail 清除登录邮箱、改回手机验证码登录。
	deviceC := newClient()
	if err := deviceC.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(deviceC)

		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: phone, APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return err
		}
		sentCode := sent.(*tg.AuthSentCode)
		if _, ok := sentCode.Type.(*tg.AuthSentCodeTypeEmailCode); !ok {
			return fmt.Errorf("pre-reset sendCode type = %T, want email code", sentCode.Type)
		}

		// 重置登录邮箱：返回一个新的手机验证码 sentCode（sentCodeTypeApp）。
		resetRes, err := raw.AuthResetLoginEmail(ctx, &tg.AuthResetLoginEmailRequest{PhoneNumber: phone, PhoneCodeHash: sentCode.PhoneCodeHash})
		if err != nil {
			return err
		}
		resetSent, ok := resetRes.(*tg.AuthSentCode)
		if !ok {
			return fmt.Errorf("resetLoginEmail result = %T, want *tg.AuthSentCode", resetRes)
		}
		if _, ok := resetSent.Type.(*tg.AuthSentCodeTypeApp); !ok {
			return fmt.Errorf("resetLoginEmail sentCode type = %T, want *tg.AuthSentCodeTypeApp (back to phone)", resetSent.Type)
		}

		// 用手机验证码完成登录。
		signInRes, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: phone, PhoneCodeHash: resetSent.PhoneCodeHash, PhoneCode: code})
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
