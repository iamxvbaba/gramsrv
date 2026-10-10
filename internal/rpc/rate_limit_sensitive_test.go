package rpc

import (
	"context"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
)

type rateWindowCall struct {
	key    string
	cost   int
	limit  int
	window time.Duration
}

type windowRateLimiter struct {
	mu     sync.Mutex
	counts map[string]int
	resets map[string]time.Time
	calls  []rateWindowCall
}

func newWindowRateLimiter() *windowRateLimiter {
	return &windowRateLimiter{
		counts: map[string]int{},
		resets: map[string]time.Time{},
	}
}

func (l *windowRateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error) {
	return l.AllowN(ctx, key, 1, limit, window)
}

func (l *windowRateLimiter) AllowN(_ context.Context, key string, cost, limit int, window time.Duration) (bool, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, rateWindowCall{key: key, cost: cost, limit: limit, window: window})
	now := time.Now()
	if now.After(l.resets[key]) {
		l.counts[key] = 0
		l.resets[key] = now.Add(window)
	}
	l.counts[key] += cost
	if l.counts[key] > limit {
		return false, int(time.Until(l.resets[key]).Seconds()) + 1, nil
	}
	return true, 0, nil
}

func TestSensitiveRPCRateLimitGroupTable(t *testing.T) {
	want := map[uint32]sensitiveRateGroup{
		tg.AuthCheckPasswordRequestTypeID:                    rateGroupSRP,
		tg.AccountGetPasswordRequestTypeID:                   rateGroupSRP,
		tg.AccountUpdatePasswordSettingsRequestTypeID:        rateGroupSRP,
		tg.AccountDeleteAccountRequestTypeID:                 rateGroupSRP,
		tg.MessagesEditChatCreatorRequestTypeID:              rateGroupSRP,
		tg.PaymentsGetStarGiftWithdrawalURLRequestTypeID:     rateGroupWithdrawal,
		tg.PaymentsGetStarsRevenueWithdrawalURLRequestTypeID: rateGroupWithdrawal,
		tg.PaymentsSendStarsFormRequestTypeID:                rateGroupPayment,
		tg.PaymentsSendPaymentFormRequestTypeID:              rateGroupPayment,
		tg.PaymentsUpgradeStarGiftRequestTypeID:              rateGroupPayment,
		tg.PaymentsTransferStarGiftRequestTypeID:             rateGroupPayment,
		tg.PaymentsSendStarGiftOfferRequestTypeID:            rateGroupPayment,
		tg.PaymentsResolveStarGiftOfferRequestTypeID:         rateGroupPayment,
		tg.PaymentsUpdateStarGiftPriceRequestTypeID:          rateGroupPayment,
		tg.PaymentsCraftStarGiftRequestTypeID:                rateGroupPayment,
		tg.PaymentsSaveStarGiftRequestTypeID:                 rateGroupPayment,
		tg.PaymentsConvertStarGiftRequestTypeID:              rateGroupPayment,
	}
	if len(sensitiveRPCRateGroups) != len(want) {
		t.Fatalf("group table size = %d, want %d", len(sensitiveRPCRateGroups), len(want))
	}
	for id, group := range want {
		if got, ok := sensitiveRPCRateGroups[id]; !ok || got != group {
			t.Errorf("method %#x group = %v ok=%v, want %v", id, got, ok, group)
		}
	}
	for _, id := range []uint32{
		tg.HelpGetConfigRequestTypeID,
		tg.MessagesSendMessageRequestTypeID,
		tg.AuthSendCodeRequestTypeID,
		tg.PaymentsGetPaymentFormRequestTypeID,
	} {
		if group, ok := sensitiveRPCRateGroups[id]; ok {
			t.Errorf("method %#x unexpectedly grouped as %v", id, group)
		}
	}
}

func TestCheckSensitiveRPCRateLimitAllowsBudgetThenFloodWaits(t *testing.T) {
	limiter := newWindowRateLimiter()
	r := New(Config{
		SRPRateLimit:  2,
		SRPRateWindow: time.Minute,
	}, Deps{Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	ctx := WithUserID(context.Background(), 7)
	for i := 0; i < 2; i++ {
		if err := r.checkSensitiveRPCRateLimit(ctx, tg.AuthCheckPasswordRequestTypeID); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	err := r.checkSensitiveRPCRateLimit(ctx, tg.AuthCheckPasswordRequestTypeID)
	if err == nil || !strings.Contains(err.Error(), "FLOOD_WAIT") {
		t.Fatalf("third call err = %v, want FLOOD_WAIT", err)
	}
	if len(limiter.calls) != 3 {
		t.Fatalf("limiter calls = %d, want 3", len(limiter.calls))
	}
	for _, call := range limiter.calls {
		if call.key != "auth:srp:u:7" || call.limit != 2 || call.window != time.Minute {
			t.Fatalf("limiter call = %+v, want key auth:srp:u:7 limit 2 window 1m", call)
		}
	}
}

func TestCheckSensitiveRPCRateLimitPreAuthFallsBackToAuthKey(t *testing.T) {
	limiter := newWindowRateLimiter()
	r := New(Config{SRPRateLimit: 5}, Deps{Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	rawAuthKeyID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	if err := r.checkSensitiveRPCRateLimit(
		WithRawAuthKeyID(context.Background(), rawAuthKeyID),
		tg.AccountGetPasswordRequestTypeID); err != nil {
		t.Fatalf("pre-auth call: %v", err)
	}
	wantKey := "auth:srp:k:" + hex.EncodeToString(rawAuthKeyID[:])
	if len(limiter.calls) != 1 || limiter.calls[0].key != wantKey {
		t.Fatalf("limiter calls = %+v, want key %q", limiter.calls, wantKey)
	}

	preferUser := WithUserID(WithRawAuthKeyID(context.Background(), rawAuthKeyID), 9)
	if err := r.checkSensitiveRPCRateLimit(preferUser, tg.AccountGetPasswordRequestTypeID); err != nil {
		t.Fatalf("authorized call: %v", err)
	}
	if limiter.calls[1].key != "auth:srp:u:9" {
		t.Fatalf("authorized key = %q, want auth:srp:u:9", limiter.calls[1].key)
	}

	if err := r.checkSensitiveRPCRateLimit(context.Background(), tg.AuthCheckPasswordRequestTypeID); err != nil {
		t.Fatalf("subject-less call: %v", err)
	}
	if len(limiter.calls) != 2 {
		t.Fatalf("subject-less call spent budget: %+v", limiter.calls)
	}
}

func TestCheckSensitiveRPCRateLimitDisabledWhenUnset(t *testing.T) {
	limiter := &captureRateLimiter{}
	r := New(Config{}, Deps{Limiter: limiter}, zaptest.NewLogger(t), clock.System)
	if err := r.checkSensitiveRPCRateLimit(
		WithUserID(context.Background(), 7),
		tg.PaymentsSendPaymentFormRequestTypeID); err != nil {
		t.Fatalf("unset limit: %v", err)
	}
	if len(limiter.calls) != 0 {
		t.Fatalf("limiter calls = %+v, want none", limiter.calls)
	}

	nilLimiter := New(Config{SRPRateLimit: 5}, Deps{}, zaptest.NewLogger(t), clock.System)
	if err := nilLimiter.checkSensitiveRPCRateLimit(
		WithUserID(context.Background(), 7),
		tg.AuthCheckPasswordRequestTypeID); err != nil {
		t.Fatalf("nil limiter: %v", err)
	}
}

func TestCheckSensitiveRPCRateLimitPerGroupBudgets(t *testing.T) {
	limiter := newWindowRateLimiter()
	r := New(Config{
		SRPRateLimit:         1,
		SRPRateWindow:        30 * time.Second,
		WithdrawalRateLimit:  2,
		WithdrawalRateWindow: time.Minute,
		PaymentRateLimit:     3,
		PaymentRateWindow:    2 * time.Minute,
	}, Deps{Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	ctx := WithUserID(context.Background(), 42)
	cases := []struct {
		id     uint32
		prefix string
		limit  int
		window time.Duration
	}{
		{tg.AccountUpdatePasswordSettingsRequestTypeID, "auth:srp:", 1, 30 * time.Second},
		{tg.PaymentsGetStarGiftWithdrawalURLRequestTypeID, "payments:withdrawal:", 2, time.Minute},
		{tg.PaymentsTransferStarGiftRequestTypeID, "payments:move:", 3, 2 * time.Minute},
	}
	for _, want := range cases {
		if err := r.checkSensitiveRPCRateLimit(ctx, want.id); err != nil {
			t.Fatalf("method %#x: %v", want.id, err)
		}
		got := limiter.calls[len(limiter.calls)-1]
		if !strings.HasPrefix(got.key, want.prefix) || got.limit != want.limit || got.window != want.window {
			t.Fatalf("method %#x limiter call = %+v, want prefix %q limit %d window %v",
				want.id, got, want.prefix, want.limit, want.window)
		}
	}
}

func TestCheckSensitiveRPCRateLimitWindowResets(t *testing.T) {
	limiter := newWindowRateLimiter()
	r := New(Config{
		SRPRateLimit:  1,
		SRPRateWindow: 100 * time.Millisecond,
	}, Deps{Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	ctx := WithUserID(context.Background(), 7)
	if err := r.checkSensitiveRPCRateLimit(ctx, tg.AuthCheckPasswordRequestTypeID); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := r.checkSensitiveRPCRateLimit(ctx, tg.AuthCheckPasswordRequestTypeID); err == nil ||
		!strings.Contains(err.Error(), "FLOOD_WAIT") {
		t.Fatalf("second call err = %v, want FLOOD_WAIT", err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := r.checkSensitiveRPCRateLimit(ctx, tg.AuthCheckPasswordRequestTypeID); err != nil {
		t.Fatalf("post-window call: %v", err)
	}
}

func TestCheckSensitiveRPCRateLimitConcurrent(t *testing.T) {
	limiter := newWindowRateLimiter()
	r := New(Config{
		SRPRateLimit:  5,
		SRPRateWindow: time.Minute,
	}, Deps{Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	ctx := WithUserID(context.Background(), 7)
	const workers = 50
	var wg sync.WaitGroup
	allowed := make(chan struct{}, workers)
	blocked := make(chan struct{}, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.checkSensitiveRPCRateLimit(ctx, tg.AuthCheckPasswordRequestTypeID); err == nil {
				allowed <- struct{}{}
			} else {
				blocked <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(allowed)
	close(blocked)
	if got := len(allowed); got != 5 {
		t.Fatalf("allowed = %d, want exactly the budget of 5", got)
	}
	if got := len(blocked); got != workers-5 {
		t.Fatalf("blocked = %d, want %d", got, workers-5)
	}
}

func TestDispatchSensitiveRPCRateLimitedBeforeHandler(t *testing.T) {
	limiter := newWindowRateLimiter()
	r := New(Config{
		SRPRateLimit:  1,
		SRPRateWindow: time.Minute,
	}, Deps{Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	ctx := WithUserID(context.Background(), 42)
	var req bin.Buffer
	(&tg.AccountGetPasswordRequest{}).Encode(&req)

	enc, err := r.Dispatch(ctx, [8]byte{1}, 1001, &req)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if _, ok := enc.(*tg.AccountPassword); !ok {
		t.Fatalf("first dispatch result = %T, want *tg.AccountPassword", enc)
	}

	var retry bin.Buffer
	(&tg.AccountGetPasswordRequest{}).Encode(&retry)
	_, err = r.Dispatch(ctx, [8]byte{1}, 1001, &retry)
	if err == nil || !strings.Contains(err.Error(), "FLOOD_WAIT") {
		t.Fatalf("second dispatch err = %v, want FLOOD_WAIT", err)
	}
}
