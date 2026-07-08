package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type testLoginEmailStore struct {
	emails map[string]string
}

func (s *testLoginEmailStore) LoginEmailByPhone(_ context.Context, phone string) (string, bool, error) {
	email, ok := s.emails[domain.NormalizePhone(phone)]
	return email, ok, nil
}

func (s *testLoginEmailStore) SetLoginEmailByPhone(_ context.Context, phone, email string) error {
	s.emails[domain.NormalizePhone(phone)] = email
	return nil
}

type testMailSender struct {
	to   string
	code string
}

func (s *testMailSender) SendLoginCode(_ context.Context, to, code string, _ time.Duration) error {
	s.to = to
	s.code = code
	return nil
}

func TestConfiguredEmailLoginSendsAndLimitsAttempts(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	if _, err := users.Create(ctx, domain.User{Phone: "15550009101", FirstName: "Email"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	emails := &testLoginEmailStore{emails: map[string]string{"15550009101": "alice@example.test"}}
	sender := &testMailSender{}
	svc := NewService(users, authz, memory.NewCodeStore(), nil, nil, "12345",
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
	if _, _, _, err := svc.SignInWithEmail(ctx, domain.Authorization{}, "+15550009101", hash, bad1); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("first bad SignInWithEmail err = %v, want ErrCodeInvalid", err)
	}
	if _, _, _, err := svc.SignInWithEmail(ctx, domain.Authorization{}, "+15550009101", hash, bad2); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("second bad SignInWithEmail err = %v, want ErrCodeInvalid", err)
	}
	if _, _, _, err := svc.SignInWithEmail(ctx, domain.Authorization{}, "+15550009101", hash, sender.code); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("SignInWithEmail after max attempts err = %v, want ErrCodeExpired", err)
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

func TestConfiguredEmailLoginAcceptsCorrectCode(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	u, err := users.Create(ctx, domain.User{Phone: "15550009102", FirstName: "Email"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	emails := &testLoginEmailStore{emails: map[string]string{"15550009102": "bob@example.test"}}
	sender := &testMailSender{}
	var key [8]byte
	key[0] = 0x91
	svc := NewService(users, authz, memory.NewCodeStore(), nil, nil, "12345",
		WithLoginEmail(LoginEmailOptions{
			Enabled:    true,
			CodeLength: 5,
			Store:      emails,
			Sender:     sender,
		}))

	hash, err := svc.SendCode(ctx, "+15550009102")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	got, _, needSignUp, err := svc.SignInWithEmail(ctx, domain.Authorization{AuthKeyID: key}, "+15550009102", hash, sender.code)
	if err != nil {
		t.Fatalf("SignInWithEmail: %v", err)
	}
	if needSignUp || got.ID != u.ID {
		t.Fatalf("SignInWithEmail got user=%d needSignUp=%v, want %d/false", got.ID, needSignUp, u.ID)
	}
}
