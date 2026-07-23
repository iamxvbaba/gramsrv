package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"telesrv/internal/app/serverlocales"
	"telesrv/internal/domain"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type captureLoginCodeDelivery struct {
	requests []domain.LoginCodeDeliveryRequest
	result   domain.LoginCodeDeliveryResult
	err      error
	failAt   int
}

func (d *captureLoginCodeDelivery) DeliverLoginCodeMessage(_ context.Context, req domain.LoginCodeDeliveryRequest) (domain.LoginCodeDeliveryResult, error) {
	d.requests = append(d.requests, req)
	if d.err != nil && (d.failAt == 0 || len(d.requests) == d.failAt) {
		return domain.LoginCodeDeliveryResult{}, d.err
	}
	return d.result, nil
}

type trackingCodeStore struct {
	store.CodeStore
	lastSetHash string
	deleted     []string
	deleteCtx   []error
	deleteErr   error
}

func (s *trackingCodeStore) Set(ctx context.Context, hash string, code store.PhoneCode, ttl time.Duration) error {
	s.lastSetHash = hash
	return s.CodeStore.Set(ctx, hash, code, ttl)
}

func (s *trackingCodeStore) Del(ctx context.Context, hash string) error {
	s.deleted = append(s.deleted, hash)
	s.deleteCtx = append(s.deleteCtx, ctx.Err())
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.CodeStore.Del(ctx, hash)
}

func TestExistingAccountSendCodeDeliversBeforeSignInAndDoesNotRedeliver(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	codes := memory.NewCodeStore()
	u, err := users.Create(ctx, domain.User{Phone: "15550009201", FirstName: "Existing"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	delivery := &captureLoginCodeDelivery{result: domain.LoginCodeDeliveryResult{Created: true}}
	svc := NewService(users, authz, codes, nil, nil, "12345", WithLoginCodeDelivery(delivery))

	before := int(time.Now().Unix())
	hash, err := svc.SendCode(ctx, "+1 555 000 9201")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if hash == "" || len(delivery.requests) != 1 {
		t.Fatalf("SendCode hash=%q delivery calls=%d, want non-empty/1", hash, len(delivery.requests))
	}
	req := delivery.requests[0]
	if req.UserID != u.ID || req.PhoneCodeHash != hash || req.Code != "12345" || req.Date < before || req.ExpiresAt < int64(before)+int64((5*time.Minute)/time.Second)-1 {
		t.Fatalf("delivery request = %+v, want user=%d hash=%q code=12345 date>=%d", req, u.ID, hash, before)
	}
	if rec, found, err := codes.Get(ctx, hash); err != nil || !found || rec.Code != "12345" {
		t.Fatalf("code after synchronous delivery = %+v found=%v err=%v", rec, found, err)
	}

	var key [8]byte
	key[0] = 0x92
	got, lateMessage, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, "+15550009201", hash, "12345")
	if err != nil || needSignUp || got.ID != u.ID {
		t.Fatalf("SignIn user=%d needSignUp=%v err=%v, want %d/false", got.ID, needSignUp, err, u.ID)
	}
	if lateMessage.ID != 0 || len(delivery.requests) != 1 {
		t.Fatalf("SignIn lateMessage=%+v delivery calls=%d, want zero/unchanged", lateMessage, len(delivery.requests))
	}
}

func TestExistingAccountSendCodeUsesServerLocale(t *testing.T) {
	ctx := serverlocales.WithLanguage(context.Background(), "ru")
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	codes := memory.NewCodeStore()
	u, err := users.Create(context.Background(), domain.User{Phone: "15550009221", FirstName: "Existing"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	delivery := &captureLoginCodeDelivery{result: domain.LoginCodeDeliveryResult{Created: true}}
	svc := NewService(users, authz, codes, nil, nil, "52342", WithLoginCodeDelivery(delivery))

	hash, err := svc.SendCode(ctx, "+1 555 000 9221")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if hash == "" || len(delivery.requests) != 1 {
		t.Fatalf("SendCode hash=%q delivery calls=%d, want non-empty/1", hash, len(delivery.requests))
	}
	req := delivery.requests[0]
	if req.UserID != u.ID || !strings.Contains(req.Body, "Код для входа в Telesrv: 52342") {
		t.Fatalf("delivery request = %+v, want Russian login text for user %d", req, u.ID)
	}
	if len(req.Entities) != 2 {
		t.Fatalf("entities = %+v, want title/code bold", req.Entities)
	}
}

func TestDeliveredLoginCodeSurvivesWrongSignInAndCancelWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	u, err := users.Create(ctx, domain.User{Phone: "15550009208", FirstName: "Cancel"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	codes := memory.NewCodeStore()
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	events := memory.NewUpdateEventStore()
	delivery := memory.NewLoginCodeDeliveryStore(messages, events)
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345", WithLoginCodeDelivery(delivery))

	hash, err := svc.SendCode(ctx, "15550009208")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	assertFacts := func(stage string) {
		t.Helper()
		history, historyErr := messages.ListByUser(ctx, u.ID, domain.MessageFilter{
			HasPeer: true,
			Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
			Limit:   10,
		})
		durable, eventErr := events.ListAfter(ctx, u.ID, 0, 10)
		if historyErr != nil || eventErr != nil || len(history.Messages) != 1 || len(durable) != 1 {
			t.Fatalf("%s messages=%d events=%d historyErr=%v eventErr=%v, want 1/1", stage, len(history.Messages), len(durable), historyErr, eventErr)
		}
	}
	assertFacts("after SendCode")

	if _, late, _, err := svc.SignIn(ctx, domain.Authorization{}, "15550009208", hash, "00000"); !errors.Is(err, ErrCodeInvalid) || late.ID != 0 {
		t.Fatalf("wrong SignIn late=%+v err=%v, want ErrCodeInvalid/no message", late, err)
	}
	assertFacts("after wrong SignIn")

	if err := svc.CancelCode(ctx, "15550009208", hash); err != nil {
		t.Fatalf("CancelCode: %v", err)
	}
	assertFacts("after CancelCode")
	if _, late, _, err := svc.SignIn(ctx, domain.Authorization{}, "15550009208", hash, "12345"); !errors.Is(err, ErrCodeExpired) || late.ID != 0 {
		t.Fatalf("SignIn after cancel late=%+v err=%v, want ErrCodeExpired/no message", late, err)
	}
	assertFacts("after canceled SignIn")
}

func TestCodeIssuedBeforeConcurrentOwnerCreationIsRejected(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	authz := memory.NewAuthorizationStore()
	codes := memory.NewCodeStore()
	delivery := &captureLoginCodeDelivery{}
	svc := NewService(users, authz, codes, nil, nil, "12345", WithLoginCodeDelivery(delivery))

	hash, err := svc.SendCode(ctx, "15550009209")
	if err != nil {
		t.Fatalf("SendCode before signup: %v", err)
	}
	rec, found, err := codes.Get(ctx, hash)
	if err != nil || !found || rec.Version != store.PhoneCodeVersionCurrent || rec.IssuedUserID != 0 || rec.SignUpVerified || len(delivery.requests) != 0 {
		t.Fatalf("pre-signup code=%+v found=%v err=%v deliveries=%d", rec, found, err, len(delivery.requests))
	}
	u, err := users.Create(ctx, domain.User{Phone: "15550009209", FirstName: "Concurrent"})
	if err != nil {
		t.Fatalf("concurrent create user: %v", err)
	}
	var key [8]byte
	key[0] = 0x93
	got, lateMessage, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, "15550009209", hash, "12345")
	if !errors.Is(err, ErrCodeInvalid) || needSignUp || got.ID != 0 || lateMessage.ID != 0 {
		t.Fatalf("SignIn after owner creation got=%+v late=%+v needSignUp=%v err=%v, want invalid", got, lateMessage, needSignUp, err)
	}
	if len(delivery.requests) != 0 {
		t.Fatalf("owner-transfer code was delivered to new owner: %+v", delivery.requests)
	}
	if bound, ok, err := svc.UserID(ctx, key); err != nil || ok || bound != 0 {
		t.Fatalf("bound user=%d ok=%v err=%v, want no authorization (created uid=%d)", bound, ok, err, u.ID)
	}
}

func TestExistingAccountRepeatedSendCodeDeliversEachIssuedHashWithoutSignIn(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	if _, err := users.Create(ctx, domain.User{Phone: "15550009202"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	delivery := &captureLoginCodeDelivery{}
	svc := NewService(users, memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345", WithLoginCodeDelivery(delivery))

	first, err := svc.SendCode(ctx, "15550009202")
	if err != nil {
		t.Fatalf("first SendCode: %v", err)
	}
	second, err := svc.SendCode(ctx, "15550009202")
	if err != nil {
		t.Fatalf("second SendCode: %v", err)
	}
	if first == second || len(delivery.requests) != 2 {
		t.Fatalf("hashes=%q/%q delivery calls=%d, want distinct/2", first, second, len(delivery.requests))
	}
	if delivery.requests[0].PhoneCodeHash != first || delivery.requests[1].PhoneCodeHash != second {
		t.Fatalf("delivery hashes = %q/%q, want %q/%q", delivery.requests[0].PhoneCodeHash, delivery.requests[1].PhoneCodeHash, first, second)
	}
}

func TestExistingAccountSendCodeDeliveryFailureRevokesCode(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	if _, err := users.Create(ctx, domain.User{Phone: "15550009203"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	baseCodes := memory.NewCodeStore()
	codes := &trackingCodeStore{CodeStore: baseCodes}
	deliveryCause := errors.New("durable write failed")
	delivery := &captureLoginCodeDelivery{err: deliveryCause}
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345", WithLoginCodeDelivery(delivery))

	hash, err := svc.SendCode(ctx, "15550009203")
	if hash != "" || !errors.Is(err, ErrLoginCodeDeliveryFailed) || !errors.Is(err, deliveryCause) {
		t.Fatalf("SendCode hash=%q err=%v, want empty ErrLoginCodeDeliveryFailed+cause", hash, err)
	}
	if codes.lastSetHash == "" || len(codes.deleted) != 1 || codes.deleted[0] != codes.lastSetHash {
		t.Fatalf("set hash=%q deleted=%v, want exact rollback", codes.lastSetHash, codes.deleted)
	}
	if _, found, getErr := baseCodes.Get(ctx, codes.lastSetHash); getErr != nil || found {
		t.Fatalf("rolled-back hash found=%v err=%v", found, getErr)
	}
}

func TestExistingAccountAmbiguousDeliveryPreservesCodeForIdempotentRetry(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	if _, err := users.Create(ctx, domain.User{Phone: "15550009213"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	baseCodes := memory.NewCodeStore()
	codes := &trackingCodeStore{CodeStore: baseCodes}
	delivery := &captureLoginCodeDelivery{err: domain.ErrLoginCodeDeliveryCommitAmbiguous}
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345", WithLoginCodeDelivery(delivery))

	hash, err := svc.SendCode(ctx, "15550009213")
	if hash != "" || !errors.Is(err, ErrLoginCodeDeliveryFailed) || !errors.Is(err, domain.ErrLoginCodeDeliveryCommitAmbiguous) {
		t.Fatalf("SendCode hash=%q err=%v, want ambiguous delivery failure", hash, err)
	}
	if codes.lastSetHash == "" || len(codes.deleted) != 0 {
		t.Fatalf("ambiguous delivery set=%q deleted=%v, want code preserved", codes.lastSetHash, codes.deleted)
	}
	if rec, found, getErr := baseCodes.Get(ctx, codes.lastSetHash); getErr != nil || !found || rec.Code != "12345" {
		t.Fatalf("ambiguous delivery code=%+v found=%v err=%v", rec, found, getErr)
	}
}

func TestExplicitDeliveryFailureRollsBackWithDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	users := memory.NewUserStore()
	if _, err := users.Create(context.Background(), domain.User{Phone: "15550009214"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	baseCodes := memory.NewCodeStore()
	codes := &trackingCodeStore{CodeStore: baseCodes}
	delivery := &captureLoginCodeDelivery{err: errors.New("definite rollback")}
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345", WithLoginCodeDelivery(delivery))

	if hash, err := svc.SendCode(ctx, "15550009214"); hash != "" || !errors.Is(err, ErrLoginCodeDeliveryFailed) {
		t.Fatalf("SendCode hash=%q err=%v, want definite failure", hash, err)
	}
	if len(codes.deleted) != 1 || len(codes.deleteCtx) != 1 || codes.deleteCtx[0] != nil {
		t.Fatalf("rollback deleted=%v ctxErr=%v, want one detached delete", codes.deleted, codes.deleteCtx)
	}
	if _, found, err := baseCodes.Get(context.Background(), codes.lastSetHash); err != nil || found {
		t.Fatalf("detached rollback found=%v err=%v", found, err)
	}
}

func TestExistingAccountMissingDeliveryFailsClosedAndRevokesCode(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	if _, err := users.Create(ctx, domain.User{Phone: "15550009204"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	baseCodes := memory.NewCodeStore()
	codes := &trackingCodeStore{CodeStore: baseCodes}
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345")

	hash, err := svc.SendCode(ctx, "15550009204")
	if hash != "" || !errors.Is(err, ErrLoginCodeDeliveryUnavailable) {
		t.Fatalf("SendCode hash=%q err=%v, want unavailable", hash, err)
	}
	if codes.lastSetHash == "" {
		t.Fatal("missing delivery was checked before code creation; want rollback path covered")
	}
	if _, found, getErr := baseCodes.Get(ctx, codes.lastSetHash); getErr != nil || found {
		t.Fatalf("unavailable delivery hash found=%v err=%v", found, getErr)
	}
}

func TestExistingAccountResendDeliversNewHashAndInvalidatesOld(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	if _, err := users.Create(ctx, domain.User{Phone: "15550009205"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	codes := memory.NewCodeStore()
	delivery := &captureLoginCodeDelivery{}
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345", WithLoginCodeDelivery(delivery))

	oldHash, err := svc.SendCode(ctx, "15550009205")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	newHash, err := svc.ResendCode(ctx, "15550009205", oldHash)
	if err != nil {
		t.Fatalf("ResendCode: %v", err)
	}
	if oldHash == newHash || len(delivery.requests) != 2 || delivery.requests[1].PhoneCodeHash != newHash {
		t.Fatalf("old/new=%q/%q deliveries=%+v", oldHash, newHash, delivery.requests)
	}
	if _, found, err := codes.Get(ctx, oldHash); err != nil || found {
		t.Fatalf("old code found=%v err=%v", found, err)
	}
	if _, found, err := codes.Get(ctx, newHash); err != nil || !found {
		t.Fatalf("new code found=%v err=%v", found, err)
	}
}

func TestExistingAccountResendDeliveryFailureLeavesNoUsableCode(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	if _, err := users.Create(ctx, domain.User{Phone: "15550009206"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	codes := memory.NewCodeStore()
	delivery := &captureLoginCodeDelivery{err: errors.New("second delivery failed"), failAt: 2}
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345", WithLoginCodeDelivery(delivery))

	oldHash, err := svc.SendCode(ctx, "15550009206")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	newHash, err := svc.ResendCode(ctx, "15550009206", oldHash)
	if newHash != "" || !errors.Is(err, ErrLoginCodeDeliveryFailed) || len(delivery.requests) != 2 {
		t.Fatalf("ResendCode hash=%q err=%v deliveries=%d", newHash, err, len(delivery.requests))
	}
	failedHash := delivery.requests[1].PhoneCodeHash
	for _, hash := range []string{oldHash, failedHash} {
		if _, found, getErr := codes.Get(ctx, hash); getErr != nil || found {
			t.Fatalf("failed resend hash %q found=%v err=%v", hash, found, getErr)
		}
	}
}

func TestConfiguredEmailLoginMirrorsSameCodeThroughAppDelivery(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	if _, err := users.Create(ctx, domain.User{Phone: "15550009207"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	emails := &testLoginEmailStore{emails: map[string]string{"15550009207": "secure@example.test"}}
	mailSender := &testMailSender{}
	delivery := &captureLoginCodeDelivery{}
	svc := NewService(users, memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		WithLoginEmail(LoginEmailOptions{Enabled: true, CodeLength: 6, Store: emails, Sender: mailSender}),
		WithLoginCodeDelivery(delivery),
	)

	if _, err := svc.SendCode(ctx, "15550009207"); err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if mailSender.to != "secure@example.test" || mailSender.code == "" {
		t.Fatalf("email delivery = %q/%q", mailSender.to, mailSender.code)
	}
	if len(delivery.requests) != 1 || delivery.requests[0].Code != mailSender.code || delivery.requests[0].PhoneCodeHash == "" {
		t.Fatalf("email App-code delivery=%+v, want same code and non-empty hash", delivery.requests)
	}
}

func TestConfiguredEmailLoginProviderFailureKeepsDurableAppCode(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	user, err := users.Create(ctx, domain.User{Phone: "15550009215"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	emails := &testLoginEmailStore{emails: map[string]string{user.Phone: "fallback@example.test"}}
	codes := memory.NewCodeStore()
	mailSender := &captureOTPSender{err: &otpdelivery.RejectedError{StatusCode: 503, Code: "UNAVAILABLE", Retryable: true}}
	delivery := &captureLoginCodeDelivery{}
	var observed []error
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithLoginEmail(LoginEmailOptions{Enabled: true, CodeLength: 6, Store: emails, Sender: mailSender}),
		WithLoginCodeDelivery(delivery),
		WithOTPDeliveryFailureObserver(func(_ context.Context, _ otpdelivery.Request, err error) {
			observed = append(observed, err)
		}),
	)

	hash, err := svc.SendCode(ctx, user.Phone)
	if err != nil || hash == "" {
		t.Fatalf("SendCode hash=%q err=%v, want App fallback success", hash, err)
	}
	if len(delivery.requests) != 1 || len(mailSender.requests) != 1 || len(observed) != 1 ||
		delivery.requests[0].Code != mailSender.requests[0].Code {
		t.Fatalf("App=%+v provider=%+v observed=%d", delivery.requests, mailSender.requests, len(observed))
	}
	if rec, found, getErr := codes.Get(ctx, hash); getErr != nil || !found || rec.Code != delivery.requests[0].Code {
		t.Fatalf("code=%+v found=%v err=%v", rec, found, getErr)
	}
}
