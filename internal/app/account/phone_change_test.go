package account

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type phoneChangeFixture struct {
	ctx       context.Context
	service   *Service
	users     *memory.UserStore
	auths     *memory.AuthorizationStore
	codes     *memory.CodeStore
	events    *memory.UpdateEventStore
	user      domain.User
	authKeyID [8]byte
	changes   *recordingPhoneChangeStore
}

type recordingPhoneChangeStore struct {
	mu    sync.Mutex
	inner store.PhoneChangeStore
	last  domain.PhoneChangeRequest
}

type trackingPhoneCodeStore struct {
	store.CodeStore
	lastHash string
}

func (s *trackingPhoneCodeStore) Set(ctx context.Context, hash string, code store.PhoneCode, ttl time.Duration) error {
	s.lastHash = hash
	return s.CodeStore.Set(ctx, hash, code, ttl)
}

func (s *recordingPhoneChangeStore) ChangePhone(ctx context.Context, req domain.PhoneChangeRequest) (domain.PhoneChangeResult, error) {
	s.mu.Lock()
	s.last = req
	s.mu.Unlock()
	return s.inner.ChangePhone(ctx, req)
}

func (s *recordingPhoneChangeStore) lastRequest() domain.PhoneChangeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func newPhoneChangeFixture(t *testing.T) phoneChangeFixture {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	auths := memory.NewAuthorizationStore()
	codes := memory.NewCodeStore()
	events := memory.NewUpdateEventStore()
	u, err := users.Create(ctx, domain.User{AccessHash: 101, Phone: "15550012001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	authKeyID := [8]byte{1, 2, 3, 4}
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: authKeyID, UserID: u.ID, CreatedAt: time.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatalf("bind auth: %v", err)
	}
	changes := &recordingPhoneChangeStore{inner: memory.NewPhoneChangeStore(users, events)}
	service := NewService(
		memory.NewPasswordStore(),
		WithUsers(users),
		WithPhoneChange(changes, auths, codes, nil, "12345", time.Minute, 3),
	)
	return phoneChangeFixture{ctx: ctx, service: service, users: users, auths: auths, codes: codes, events: events, user: u, authKeyID: authKeyID, changes: changes}
}

func TestPhoneChangeWebhookDeliversRandomScopedCode(t *testing.T) {
	f := newPhoneChangeFixture(t)
	sender := &captureMailSender{}
	f.service.phoneCodeSender = sender
	f.service.phoneCodeLength = 6
	f.service.phoneChangeCode = ""

	hash, delivery, err := f.service.SendChangePhoneCode(f.ctx, f.user.ID, f.authKeyID, 77, "15550012020")
	if err != nil {
		t.Fatalf("SendChangePhoneCode: %v", err)
	}
	if hash == "" || delivery.Kind != domain.AuthCodeDeliverySMS || delivery.Length != 6 || len(sender.requests) != 1 {
		t.Fatalf("hash=%q delivery=%+v requests=%d", hash, delivery, len(sender.requests))
	}
	req := sender.requests[0]
	if req.Purpose != otpdelivery.PurposeChangePhone || req.Channel != otpdelivery.ChannelSMS || req.Recipient != "15550012020" || req.DeliveryID == "" {
		t.Fatalf("request = %+v", req)
	}
	rec, found, err := f.codes.Get(f.ctx, hash)
	if err != nil || !found || rec.Channel != store.PhoneCodeChannelSMS || rec.DeliveryID != req.DeliveryID || rec.Code != req.Code {
		t.Fatalf("record=%+v found=%v err=%v", rec, found, err)
	}
	if _, err := f.service.ChangePhone(f.ctx, f.user.ID, f.authKeyID, f.authKeyID, 78, req.Recipient, hash, req.Code, 1700000000); err != nil {
		t.Fatalf("ChangePhone: %v", err)
	}
}

func TestPhoneChangeWebhookRejectionRevokesScopedCode(t *testing.T) {
	f := newPhoneChangeFixture(t)
	sender := &captureMailSender{err: &otpdelivery.RejectedError{StatusCode: 503, Code: "UNAVAILABLE", Retryable: true}}
	tracked := &trackingPhoneCodeStore{CodeStore: f.codes}
	f.service.codes = tracked
	f.service.phoneCodeSender = sender
	f.service.phoneCodeLength = 5

	hash, _, err := f.service.SendChangePhoneCode(f.ctx, f.user.ID, f.authKeyID, 77, "15550012021")
	if hash != "" || err == nil || len(sender.requests) != 1 {
		t.Fatalf("hash=%q err=%v requests=%d", hash, err, len(sender.requests))
	}
	if tracked.lastHash == "" {
		t.Fatal("code was not stored before delivery")
	}
	if rec, found, getErr := f.codes.Get(f.ctx, tracked.lastHash); getErr != nil || found || rec.Code != "" {
		t.Fatalf("post-rejection code rec=%+v found=%v err=%v", rec, found, getErr)
	}
}

func TestPhoneChangeScopesCodeAndPersistsDurableEvent(t *testing.T) {
	f := newPhoneChangeFixture(t)
	hash, delivery, err := f.service.SendChangePhoneCode(f.ctx, f.user.ID, f.authKeyID, 77, "+1 (555) 001-2002")
	if err != nil {
		t.Fatalf("send change code: %v", err)
	}
	if hash == "" || delivery.Kind != domain.AuthCodeDeliverySMS || delivery.Length != 5 {
		t.Fatalf("delivery = hash %q %+v", hash, delivery)
	}
	rec, found, err := f.codes.Get(f.ctx, hash)
	if err != nil || !found {
		t.Fatalf("load code found=%v err=%v", found, err)
	}
	if rec.Version != store.PhoneCodeVersionCurrent || rec.Purpose != store.PhoneCodePurposeChangePhone || rec.Phone != "15550012002" || rec.UserID != f.user.ID || rec.AuthKeyID != f.authKeyID || rec.SessionID != 77 {
		t.Fatalf("scoped code = %+v", rec)
	}

	rawAuthKeyID := [8]byte{8, 8, 8, 8}
	result, err := f.service.ChangePhone(f.ctx, f.user.ID, f.authKeyID, rawAuthKeyID, 88, "+1 555 001 2002", hash, "12345", 1700000000)
	if err != nil {
		t.Fatalf("change phone after session reconnect: %v", err)
	}
	if !result.Changed || result.User.Phone != "15550012002" || result.Event.Type != domain.UpdateEventUserPhone || result.Event.Phone != "15550012002" || result.Event.Pts != 1 {
		t.Fatalf("change result = %+v", result)
	}
	if got := f.changes.lastRequest().ExcludeAuthKeyID; got != rawAuthKeyID {
		t.Fatalf("outbox exclusion auth key = %x, want physical raw %x", got, rawAuthKeyID)
	}
	if _, found, _ := f.users.ByPhone(f.ctx, "15550012001"); found {
		t.Fatal("old phone still resolves")
	}
	if got, found, _ := f.users.ByPhone(f.ctx, "15550012002"); !found || got.ID != f.user.ID {
		t.Fatalf("new phone resolves to %+v found=%v", got, found)
	}
	events, err := f.events.ListAfter(f.ctx, f.user.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].Type != domain.UpdateEventUserPhone || events[0].Phone != "15550012002" {
		t.Fatalf("durable events = %+v err=%v", events, err)
	}
	if _, found, _ := f.codes.Get(f.ctx, hash); found {
		t.Fatal("successful code was not consumed")
	}
}

func TestPhoneChangeRejectsOccupiedAndCrossAuthCode(t *testing.T) {
	f := newPhoneChangeFixture(t)
	occupied, err := f.users.Create(f.ctx, domain.User{AccessHash: 102, Phone: "15550012003", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create occupied user: %v", err)
	}
	if _, _, err := f.service.SendChangePhoneCode(f.ctx, f.user.ID, f.authKeyID, 77, occupied.Phone); !errors.Is(err, domain.ErrPhoneNumberOccupied) {
		t.Fatalf("occupied send err = %v", err)
	}

	hash, _, err := f.service.SendChangePhoneCode(f.ctx, f.user.ID, f.authKeyID, 77, "15550012004")
	if err != nil {
		t.Fatalf("send code: %v", err)
	}
	otherKey := [8]byte{9, 9, 9}
	if err := f.auths.Bind(f.ctx, domain.Authorization{AuthKeyID: otherKey, UserID: occupied.ID}); err != nil {
		t.Fatalf("bind other auth: %v", err)
	}
	if _, err := f.service.ChangePhone(f.ctx, occupied.ID, otherKey, otherKey, 99, "15550012004", hash, "12345", 0); !errors.Is(err, domain.ErrPhoneCodeExpired) {
		t.Fatalf("cross-auth change err = %v", err)
	}
	if got, found, _ := f.users.ByID(f.ctx, occupied.ID); !found || got.Phone != "15550012003" {
		t.Fatalf("other user changed = %+v found=%v", got, found)
	}
}

func TestPhoneChangeWrongCodeExhaustsAttempts(t *testing.T) {
	f := newPhoneChangeFixture(t)
	hash, _, err := f.service.SendChangePhoneCode(f.ctx, f.user.ID, f.authKeyID, 77, "15550012005")
	if err != nil {
		t.Fatalf("send code: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.service.ChangePhone(f.ctx, f.user.ID, f.authKeyID, f.authKeyID, 77, "15550012005", hash, "00000", 0); !errors.Is(err, domain.ErrPhoneCodeInvalid) {
			t.Fatalf("wrong attempt %d err = %v", i+1, err)
		}
	}
	if _, err := f.service.ChangePhone(f.ctx, f.user.ID, f.authKeyID, f.authKeyID, 77, "15550012005", hash, "12345", 0); !errors.Is(err, domain.ErrPhoneCodeExpired) {
		t.Fatalf("exhausted code err = %v", err)
	}
	if got, _, _ := f.users.ByID(f.ctx, f.user.ID); got.Phone != "15550012001" {
		t.Fatalf("phone changed after exhausted code: %q", got.Phone)
	}
}

func TestPhoneChangeNewSendInvalidatesPreviousHash(t *testing.T) {
	f := newPhoneChangeFixture(t)
	oldHash, _, err := f.service.SendChangePhoneCode(f.ctx, f.user.ID, f.authKeyID, 77, "15550012006")
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	newHash, _, err := f.service.SendChangePhoneCode(f.ctx, f.user.ID, f.authKeyID, 88, "15550012006")
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if oldHash == newHash {
		t.Fatalf("hash was not rotated: %q", oldHash)
	}
	if _, err := f.service.ChangePhone(f.ctx, f.user.ID, f.authKeyID, f.authKeyID, 99, "15550012006", oldHash, "12345", 1700000001); !errors.Is(err, domain.ErrPhoneCodeExpired) {
		t.Fatalf("old hash replay err = %v", err)
	}
	if _, err := f.service.ChangePhone(f.ctx, f.user.ID, f.authKeyID, f.authKeyID, 99, "15550012006", newHash, "12345", 1700000002); err != nil {
		t.Fatalf("new hash change: %v", err)
	}
	events, err := f.events.ListAfter(f.ctx, f.user.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].Type != domain.UpdateEventUserPhone {
		t.Fatalf("events = %+v err=%v", events, err)
	}
}

func TestPhoneChangeConcurrentReplayAppendsOneEvent(t *testing.T) {
	f := newPhoneChangeFixture(t)
	hash, _, err := f.service.SendChangePhoneCode(f.ctx, f.user.ID, f.authKeyID, 77, "15550012007")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	const workers = 24
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.service.ChangePhone(f.ctx, f.user.ID, f.authKeyID, f.authKeyID, 88, "15550012007", hash, "12345", 1700000003)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	successes := 0
	expired := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrPhoneCodeExpired):
			expired++
		default:
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if successes != 1 || expired != workers-1 {
		t.Fatalf("successes=%d expired=%d", successes, expired)
	}
	events, err := f.events.ListAfter(f.ctx, f.user.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].Pts != 1 {
		t.Fatalf("events = %+v err=%v", events, err)
	}
}
