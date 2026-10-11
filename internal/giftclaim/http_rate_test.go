package giftclaim

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

type claimWindowCall struct {
	key   string
	limit int
}

type claimWindowLimiter struct {
	mu     sync.Mutex
	counts map[string]int
	resets map[string]time.Time
	calls  []claimWindowCall
}

func newClaimWindowLimiter() *claimWindowLimiter {
	return &claimWindowLimiter{
		counts: map[string]int{},
		resets: map[string]time.Time{},
	}
}

func (l *claimWindowLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, claimWindowCall{key: key, limit: limit})
	now := time.Now()
	if now.After(l.resets[key]) {
		l.counts[key] = 0
		l.resets[key] = now.Add(window)
	}
	l.counts[key]++
	if l.counts[key] > limit {
		return false, int(time.Until(l.resets[key]).Seconds()) + 1, nil
	}
	return true, 0, nil
}

type failingClaimLimiter struct{}

func (failingClaimLimiter) Allow(context.Context, string, int, time.Duration) (bool, int, error) {
	return false, 0, errors.New("redis down")
}

func newClaimRateService(t *testing.T, limiter Limiter, apiLimit, withdrawLimit int) *Service {
	t.Helper()
	return &Service{
		basePath:           "/claim",
		limiter:            limiter,
		apiRateLimit:       apiLimit,
		apiRateWindow:      time.Minute,
		withdrawRateLimit:  withdrawLimit,
		withdrawRateWindow: time.Minute,
		logger:             zaptest.NewLogger(t),
	}
}

func postClaim(s *Service, path, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/claim"+path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if ip != "" {
		req.Header.Set("X-Real-IP", ip)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestClaimRateLimitAllowsBudgetThenBlocksPerIP(t *testing.T) {
	svc := newClaimRateService(t, newClaimWindowLimiter(), 2, 2)
	for i := 0; i < 2; i++ {
		if got := postClaim(svc, "/api/challenge", "203.0.113.9").Code; got != http.StatusUnauthorized {
			t.Fatalf("call %d status = %d, want 401", i+1, got)
		}
	}
	blocked := postClaim(svc, "/api/challenge", "203.0.113.9")
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("over-budget status = %d, want 429", blocked.Code)
	}
	if blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("429 response is missing Retry-After")
	}
	if got := postClaim(svc, "/api/challenge", "203.0.113.10").Code; got != http.StatusUnauthorized {
		t.Fatalf("other IP status = %d, want 401", got)
	}
}

func TestClaimWithdrawBucketIndependentOfAPI(t *testing.T) {
	svc := newClaimRateService(t, newClaimWindowLimiter(), 1, 2)
	if got := postClaim(svc, "/api/challenge", "203.0.113.9").Code; got != http.StatusUnauthorized {
		t.Fatalf("challenge 1 status = %d, want 401", got)
	}
	if got := postClaim(svc, "/api/challenge", "203.0.113.9").Code; got != http.StatusTooManyRequests {
		t.Fatalf("challenge over-budget status = %d, want 429", got)
	}
	for i := 0; i < 2; i++ {
		if got := postClaim(svc, "/api/wallet/password", "203.0.113.9").Code; got != http.StatusUnauthorized {
			t.Fatalf("withdraw %d status = %d, want 401 with its own bucket", i+1, got)
		}
	}
	if got := postClaim(svc, "/api/wallet/password", "203.0.113.9").Code; got != http.StatusTooManyRequests {
		t.Fatalf("withdraw over-budget status = %d, want 429", got)
	}
}

func TestClaimRateLimitWindowResets(t *testing.T) {
	svc := &Service{
		basePath:           "/claim",
		limiter:            newClaimWindowLimiter(),
		apiRateLimit:       1,
		apiRateWindow:      100 * time.Millisecond,
		withdrawRateLimit:  1,
		withdrawRateWindow: 100 * time.Millisecond,
		logger:             zaptest.NewLogger(t),
	}
	if got := postClaim(svc, "/api/challenge", "203.0.113.9").Code; got != http.StatusUnauthorized {
		t.Fatalf("first status = %d, want 401", got)
	}
	if got := postClaim(svc, "/api/challenge", "203.0.113.9").Code; got != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", got)
	}
	time.Sleep(150 * time.Millisecond)
	if got := postClaim(svc, "/api/challenge", "203.0.113.9").Code; got != http.StatusUnauthorized {
		t.Fatalf("post-window status = %d, want 401", got)
	}
}

func TestClaimRateLimitOffWhenUnconfigured(t *testing.T) {
	noLimiter := newClaimRateService(t, nil, 1, 1)
	for i := 0; i < 5; i++ {
		if got := postClaim(noLimiter, "/api/challenge", "203.0.113.9").Code; got != http.StatusUnauthorized {
			t.Fatalf("nil limiter call %d status = %d, want 401", i+1, got)
		}
	}

	limiter := newClaimWindowLimiter()
	zeroLimits := newClaimRateService(t, limiter, 0, 0)
	for i := 0; i < 5; i++ {
		if got := postClaim(zeroLimits, "/api/challenge", "203.0.113.9").Code; got != http.StatusUnauthorized {
			t.Fatalf("zero limit call %d status = %d, want 401", i+1, got)
		}
	}
	if len(limiter.calls) != 0 {
		t.Fatalf("zero limit still spent budget: %+v", limiter.calls)
	}
}

func TestClaimRateLimitSeparatesIPsAndHashesKeys(t *testing.T) {
	limiter := newClaimWindowLimiter()
	svc := newClaimRateService(t, limiter, 1, 1)
	if got := postClaim(svc, "/api/challenge", "203.0.113.9").Code; got != http.StatusUnauthorized {
		t.Fatalf("first IP status = %d, want 401", got)
	}
	if got := postClaim(svc, "/api/challenge", "203.0.113.9").Code; got != http.StatusTooManyRequests {
		t.Fatalf("first IP over-budget status = %d, want 429", got)
	}
	if got := postClaim(svc, "/api/challenge", "198.51.100.7").Code; got != http.StatusUnauthorized {
		t.Fatalf("second IP status = %d, want 401", got)
	}
	req := httptest.NewRequest(http.MethodPost, "/claim/api/challenge", strings.NewReader("{}"))
	req.Header.Set("X-Forwarded-For", "192.0.2.55, 203.0.113.9")
	rec := httptest.NewRecorder()
	svc.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("forwarded-IP status = %d, want 429 reusing the X-Real-IP budget", rec.Code)
	}
	for _, call := range limiter.calls {
		for _, leaked := range []string{"203.0.113.9", "198.51.100.7", "192.0.2.55"} {
			if strings.Contains(call.key, leaked) {
				t.Fatalf("limiter key leaked client IP: %q", call.key)
			}
		}
	}
}

func TestClaimAllPOSTEndpointsSpendBudget(t *testing.T) {
	limiter := newClaimWindowLimiter()
	svc := newClaimRateService(t, limiter, 100, 100)
	paths := []string{
		"/api/challenge",
		"/api/verify",
		"/api/mint",
		"/api/confirm",
		"/api/wallet/send",
		"/api/wallet/release",
		"/api/wallet/password",
		"/api/wallet/withdraw",
		"/api/admin/status",
		"/api/admin/export",
	}
	for _, path := range paths {
		if got := postClaim(svc, path, "203.0.113.9").Code; got == http.StatusTooManyRequests {
			t.Fatalf("%s under-budget status = 429", path)
		}
	}
	if len(limiter.calls) != len(paths) {
		t.Fatalf("limiter calls = %d, want every endpoint gated (%d)", len(limiter.calls), len(paths))
	}
}

func TestClaimRateLimitConcurrent(t *testing.T) {
	const budget = 10
	svc := newClaimRateService(t, newClaimWindowLimiter(), budget, budget)

	const workers = 40
	var wg sync.WaitGroup
	codes := make(chan int, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- postClaim(svc, "/api/challenge", "203.0.113.9").Code
		}()
	}
	wg.Wait()
	close(codes)
	passed, limited := 0, 0
	for code := range codes {
		switch code {
		case http.StatusTooManyRequests:
			limited++
		default:
			passed++
		}
	}
	if passed != budget || limited != workers-budget {
		t.Fatalf("passed = %d limited = %d, want %d/%d", passed, limited, budget, workers-budget)
	}
}

func TestClaimRateLimitLimiterErrorFailsClosed(t *testing.T) {
	svc := newClaimRateService(t, failingClaimLimiter{}, 1, 1)
	if got := postClaim(svc, "/api/challenge", "203.0.113.9").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("limiter error status = %d, want 503", got)
	}
}
