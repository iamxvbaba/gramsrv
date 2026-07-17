package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type captureOTPSender struct {
	requests []otpdelivery.Request
	err      error
	before   func()
}

func (s *captureOTPSender) Deliver(_ context.Context, req otpdelivery.Request) (otpdelivery.Result, error) {
	if s.before != nil {
		s.before()
	}
	s.requests = append(s.requests, req)
	return otpdelivery.Result{ProviderMessageID: "capture-message"}, s.err
}

func TestWebhookPhoneLoginUsesRandomSMSCode(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	user, err := users.Create(ctx, domain.User{Phone: "15550009301", FirstName: "Webhook"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	codes := memory.NewCodeStore()
	appDelivery := &captureLoginCodeDelivery{}
	sender := &captureOTPSender{before: func() {
		if len(appDelivery.requests) != 1 {
			t.Fatalf("provider called before durable App-code: requests=%d", len(appDelivery.requests))
		}
	}}
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "fixed-code-must-not-leak",
		WithLoginCodeDelivery(appDelivery),
		WithPhoneCodeDelivery(sender, 6))

	hash, err := svc.SendCode(ctx, "+1 555 000 9301")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if hash == "" || len(sender.requests) != 1 {
		t.Fatalf("hash=%q requests=%d", hash, len(sender.requests))
	}
	req := sender.requests[0]
	if req.DeliveryID == "" || req.Purpose != otpdelivery.PurposeLoginSMS || req.Channel != otpdelivery.ChannelSMS ||
		req.Recipient != "15550009301" || len(req.Code) != 6 || req.Code == "fixed-code-must-not-leak" || time.Until(req.ExpiresAt) < 4*time.Minute {
		t.Fatalf("request = %+v", req)
	}
	if len(appDelivery.requests) != 1 || appDelivery.requests[0].PhoneCodeHash != hash || appDelivery.requests[0].Code != req.Code {
		t.Fatalf("App-code delivery=%+v, want same hash/code as provider", appDelivery.requests)
	}
	rec, found, err := codes.Get(ctx, hash)
	if err != nil || !found || rec.Code != req.Code || rec.DeliveryID != req.DeliveryID || rec.Channel != store.PhoneCodeChannelSMS {
		t.Fatalf("stored code=%+v found=%v err=%v", rec, found, err)
	}
	delivery, found, err := svc.CodeDelivery(ctx, hash)
	if err != nil || !found || delivery.Kind != domain.AuthCodeDeliverySMS || delivery.Length != 6 {
		t.Fatalf("delivery=%+v found=%v err=%v", delivery, found, err)
	}
	got, _, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: [8]byte{3}}, req.Recipient, hash, req.Code)
	if err != nil || needSignUp || got.ID != user.ID {
		t.Fatalf("SignIn user=%+v needSignUp=%v err=%v", got, needSignUp, err)
	}
}

func TestWebhookExistingAccountRejectionKeepsDurableAppCode(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	user, err := users.Create(ctx, domain.User{Phone: "15550009305", FirstName: "Fallback"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	codes := memory.NewCodeStore()
	appDelivery := &captureLoginCodeDelivery{}
	sender := &captureOTPSender{err: &otpdelivery.RejectedError{StatusCode: 503, Code: "UNAVAILABLE", Retryable: true}}
	var observed []error
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithLoginCodeDelivery(appDelivery),
		WithPhoneCodeDelivery(sender, 6),
		WithOTPDeliveryFailureObserver(func(_ context.Context, _ otpdelivery.Request, err error) {
			observed = append(observed, err)
		}),
	)

	hash, err := svc.SendCode(ctx, user.Phone)
	if err != nil || hash == "" {
		t.Fatalf("SendCode hash=%q err=%v, want App fallback success", hash, err)
	}
	if len(sender.requests) != 1 || len(appDelivery.requests) != 1 || len(observed) != 1 {
		t.Fatalf("provider=%d App=%d observed=%d, want 1/1/1", len(sender.requests), len(appDelivery.requests), len(observed))
	}
	rec, found, err := codes.Get(ctx, hash)
	if err != nil || !found || rec.Code != appDelivery.requests[0].Code || rec.Code != sender.requests[0].Code {
		t.Fatalf("code=%+v found=%v err=%v App=%+v provider=%+v", rec, found, err, appDelivery.requests, sender.requests)
	}
}

func TestWebhookPhoneLoginExplicitRejectionRollsBackCode(t *testing.T) {
	ctx := context.Background()
	baseCodes := memory.NewCodeStore()
	codes := &trackingCodeStore{CodeStore: baseCodes}
	sender := &captureOTPSender{err: &otpdelivery.RejectedError{StatusCode: 503, Code: "UNAVAILABLE", Retryable: true}}
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithPhoneCodeDelivery(sender, 5))

	hash, err := svc.SendCode(ctx, "15550009302")
	if hash != "" || err == nil || len(sender.requests) != 1 || codes.lastSetHash == "" {
		t.Fatalf("hash=%q err=%v requests=%d set=%q", hash, err, len(sender.requests), codes.lastSetHash)
	}
	if _, found, getErr := baseCodes.Get(ctx, codes.lastSetHash); getErr != nil || found {
		t.Fatalf("rejected code found=%v err=%v", found, getErr)
	}
}

func TestWebhookPhoneLoginUnknownOutcomeKeepsUsableCode(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	sender := &captureOTPSender{err: &otpdelivery.OutcomeUnknownError{Cause: errors.New("response lost")}}
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithPhoneCodeDelivery(sender, 5))

	hash, err := svc.SendCode(ctx, "15550009303")
	if err != nil || hash == "" || len(sender.requests) != 1 {
		t.Fatalf("hash=%q err=%v requests=%d", hash, err, len(sender.requests))
	}
	rec, found, err := codes.Get(ctx, hash)
	if err != nil || !found || rec.Code != sender.requests[0].Code {
		t.Fatalf("unknown outcome code=%+v found=%v err=%v", rec, found, err)
	}
}

func TestWebhookPhoneResendRotatesCodeAndDeliveryID(t *testing.T) {
	ctx := context.Background()
	sender := &captureOTPSender{}
	codes := memory.NewCodeStore()
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithPhoneCodeDelivery(sender, 6))
	firstHash, err := svc.SendCode(ctx, "15550009304")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	secondHash, err := svc.ResendCode(ctx, "15550009304", firstHash)
	if err != nil {
		t.Fatalf("ResendCode: %v", err)
	}
	if firstHash == secondHash || len(sender.requests) != 2 ||
		sender.requests[0].DeliveryID == sender.requests[1].DeliveryID {
		t.Fatalf("hashes=%q/%q requests=%+v", firstHash, secondHash, sender.requests)
	}
	if _, found, err := codes.Get(ctx, firstHash); err != nil || found {
		t.Fatalf("old code found=%v err=%v", found, err)
	}
}
