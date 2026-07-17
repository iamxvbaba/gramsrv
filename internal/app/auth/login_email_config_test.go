package auth

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/store/memory"
)

type testLoginEmailStore struct {
	emails map[string]string
}

func (s *testLoginEmailStore) LoginEmailByPhone(_ context.Context, phone string) (string, bool, error) {
	email, ok := s.emails[domain.NormalizePhone(phone)]
	return email, ok, nil
}

func (s *testLoginEmailStore) SetLoginEmail(_ context.Context, _ int64, _ string) error {
	return nil
}

type testMailSender struct {
	to   string
	code string
}

func (s *testMailSender) Deliver(_ context.Context, req otpdelivery.Request) (otpdelivery.Result, error) {
	s.to = req.Recipient
	s.code = req.Code
	return otpdelivery.Result{}, nil
}

func TestConfiguredEmailLoginSharesAttemptsAcrossOfficialCodeCarriers(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	if _, err := users.Create(ctx, domain.User{Phone: "15550009101", FirstName: "Email"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	emails := &testLoginEmailStore{emails: map[string]string{"15550009101": "alice@example.test"}}
	sender := &testMailSender{}
	appDelivery := &captureLoginCodeDelivery{}
	svc := NewService(users, authz, memory.NewCodeStore(), nil, nil, "12345",
		WithLoginCodeDelivery(appDelivery),
		WithLoginEmail(LoginEmailOptions{
			Enabled:    true,
			CodeLength: 6,
			Store:      emails,
			Sender:     sender,
		}),
		WithCodeMaxAttempts(2))

	hash, err := svc.SendCode(ctx, "+1 555 000 9101")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if sender.to != "alice@example.test" || len(sender.code) != 6 {
		t.Fatalf("sent email to/code = %q/%q, want alice@example.test/6 digits", sender.to, sender.code)
	}
	if len(appDelivery.requests) != 1 || appDelivery.requests[0].PhoneCodeHash != hash || appDelivery.requests[0].Code != sender.code {
		t.Fatalf("App-code delivery=%+v, want same email code/hash", appDelivery.requests)
	}
	delivery, found, err := svc.CodeDelivery(ctx, hash)
	if err != nil || !found {
		t.Fatalf("CodeDelivery found=%v err=%v", found, err)
	}
	if delivery.Kind != domain.AuthCodeDeliveryEmail || delivery.EmailPattern != "a***e@example.test" || delivery.Length != 6 {
		t.Fatalf("delivery = %+v, want email masked length 6", delivery)
	}
	bad1 := wrongCode(sender.code, '0')
	bad2 := wrongCode(sender.code, '1')
	if bad2 == bad1 {
		bad2 = wrongCode(sender.code, '2')
	}
	if _, _, _, err := svc.SignIn(ctx, domain.Authorization{}, "+15550009101", hash, bad1); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("first bad WebK SignIn err = %v, want ErrCodeInvalid", err)
	}
	if _, _, _, err := svc.SignInWithEmail(ctx, domain.Authorization{}, "+15550009101", hash, bad2); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("second bad native SignInWithEmail err = %v, want ErrCodeInvalid", err)
	}
	if _, _, _, err := svc.SignIn(ctx, domain.Authorization{}, "+15550009101", hash, sender.code); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("WebK SignIn after shared max attempts err = %v, want ErrCodeExpired", err)
	}
}

func wrongCode(code string, digit byte) string {
	if code == "" {
		return string(digit)
	}
	out := make([]byte, len(code))
	for i := range out {
		out[i] = digit
	}
	if string(out) != code {
		return string(out)
	}
	for i := range out {
		out[i] = '9'
	}
	return string(out)
}

func TestConfiguredEmailLoginAcceptsOfficialCodeCarriers(t *testing.T) {
	tests := []struct {
		name  string
		phone string
		email string
		webK  bool
	}{
		{name: "webk_phone_code", phone: "15550009102", email: "webk@example.test", webK: true},
		{name: "native_email_verification", phone: "15550009103", email: "native@example.test"},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			users := memory.NewUserStore()
			authz := memory.NewAuthorizationStore()
			u, err := users.Create(ctx, domain.User{Phone: tc.phone, FirstName: "Email"})
			if err != nil {
				t.Fatalf("create user: %v", err)
			}
			emails := &testLoginEmailStore{emails: map[string]string{tc.phone: tc.email}}
			sender := &testMailSender{}
			appDelivery := &captureLoginCodeDelivery{}
			var key [8]byte
			key[0] = byte(0x91 + i)
			svc := NewService(users, authz, memory.NewCodeStore(), nil, nil, "12345",
				WithLoginCodeDelivery(appDelivery),
				WithLoginEmail(LoginEmailOptions{
					Enabled:    true,
					CodeLength: 6,
					Store:      emails,
					Sender:     sender,
				}))

			hash, err := svc.SendCode(ctx, tc.phone)
			if err != nil {
				t.Fatalf("SendCode: %v", err)
			}
			if len(appDelivery.requests) != 1 || appDelivery.requests[0].Code != sender.code {
				t.Fatalf("App-code delivery=%+v, want same email code", appDelivery.requests)
			}

			var got domain.User
			var needSignUp bool
			if tc.webK {
				if _, _, _, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, tc.phone, hash, "12345"); !errors.Is(err, ErrCodeInvalid) {
					t.Fatalf("WebK development code err=%v, want ErrCodeInvalid for random email channel", err)
				}
				got, _, needSignUp, err = svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, tc.phone, hash, sender.code)
			} else {
				got, _, needSignUp, err = svc.SignInWithEmail(ctx, domain.Authorization{AuthKeyID: key}, tc.phone, hash, sender.code)
			}
			if err != nil {
				t.Fatalf("sign in: %v", err)
			}
			if needSignUp || got.ID != u.ID {
				t.Fatalf("sign in got user=%d needSignUp=%v, want %d/false", got.ID, needSignUp, u.ID)
			}
		})
	}
}

func TestConfiguredEmailLoginViaWebKStillHonorsTwoFactor(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	passwords := memory.NewPasswordStore()
	u, err := users.Create(ctx, domain.User{Phone: "15550009104", FirstName: "Email"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := passwords.Save(ctx, u.ID, domain.PasswordSettings{HasPassword: true}); err != nil {
		t.Fatalf("save password settings: %v", err)
	}
	sender := &testMailSender{}
	svc := NewService(users, authz, memory.NewCodeStore(), nil, nil, "12345",
		WithPasswords(passwords),
		WithLoginCodeDelivery(&captureLoginCodeDelivery{}),
		WithLoginEmail(LoginEmailOptions{
			Enabled:    true,
			CodeLength: 6,
			Store:      &testLoginEmailStore{emails: map[string]string{u.Phone: "2fa@example.test"}},
			Sender:     sender,
		}))
	var key [8]byte
	key[0] = 0x94

	hash, err := svc.SendCode(ctx, u.Phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	got, _, _, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, u.Phone, hash, sender.code)
	if !errors.Is(err, domain.ErrSessionPasswordNeeded) {
		t.Fatalf("WebK email SignIn err=%v, want ErrSessionPasswordNeeded", err)
	}
	if got.ID != u.ID {
		t.Fatalf("WebK email SignIn user=%d, want pending 2FA user %d", got.ID, u.ID)
	}
	if bound, found, err := svc.UserID(ctx, key); err != nil || found || bound != 0 {
		t.Fatalf("UserID after WebK email SignIn with 2FA=%d found=%v err=%v, want not-found", bound, found, err)
	}
}

func TestConfiguredEmailLoginHasSingleConsumerAcrossOfficialCodeCarriers(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	u, err := users.Create(ctx, domain.User{Phone: "15550009105", FirstName: "Email"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sender := &testMailSender{}
	svc := NewService(users, memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		WithLoginCodeDelivery(&captureLoginCodeDelivery{}),
		WithLoginEmail(LoginEmailOptions{
			Enabled:    true,
			CodeLength: 6,
			Store:      &testLoginEmailStore{emails: map[string]string{u.Phone: "race@example.test"}},
			Sender:     sender,
		}))
	hash, err := svc.SendCode(ctx, u.Phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var webKKey, nativeKey [8]byte
	webKKey[0] = 0x95
	nativeKey[0] = 0x96
	go func() {
		<-start
		_, _, _, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: webKKey}, u.Phone, hash, sender.code)
		results <- err
	}()
	go func() {
		<-start
		_, _, _, err := svc.SignInWithEmail(ctx, domain.Authorization{AuthKeyID: nativeKey}, u.Phone, hash, sender.code)
		results <- err
	}()
	close(start)

	accepted, expired := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrCodeExpired):
			expired++
		default:
			t.Fatalf("concurrent sign in err=%v, want nil or ErrCodeExpired", err)
		}
	}
	if accepted != 1 || expired != 1 {
		t.Fatalf("concurrent results accepted=%d expired=%d, want 1/1", accepted, expired)
	}
}
