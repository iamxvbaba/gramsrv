package customfragment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

type giftWindowCall struct {
	key   string
	limit int
}

type giftWindowLimiter struct {
	mu     sync.Mutex
	counts map[string]int
	resets map[string]time.Time
	calls  []giftWindowCall
}

func newGiftWindowLimiter() *giftWindowLimiter {
	return &giftWindowLimiter{
		counts: map[string]int{},
		resets: map[string]time.Time{},
	}
}

func (l *giftWindowLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, giftWindowCall{key: key, limit: limit})
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

func newGiftRateService(t *testing.T, limiter Limiter, limit int) *Service {
	t.Helper()
	return &Service{
		limiter:       limiter,
		apiRateLimit:  limit,
		apiRateWindow: time.Minute,
		logger:        zaptest.NewLogger(t),
	}
}

func postGiftAPI(s *Service, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/custom-fragment/api/gifts/onepart", nil)
	if ip != "" {
		req.Header.Set("X-Real-IP", ip)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestGiftAPIRateLimitAllowsThenBlocksPerIP(t *testing.T) {
	svc := newGiftRateService(t, newGiftWindowLimiter(), 2)
	for i := 0; i < 2; i++ {
		if got := postGiftAPI(svc, "203.0.113.9").Code; got != http.StatusNotFound {
			t.Fatalf("call %d status = %d, want 404 past the gate", i+1, got)
		}
	}
	blocked := postGiftAPI(svc, "203.0.113.9")
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("over-budget status = %d, want 429", blocked.Code)
	}
	if blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("429 response is missing Retry-After")
	}
	if got := postGiftAPI(svc, "203.0.113.10").Code; got != http.StatusNotFound {
		t.Fatalf("other IP status = %d, want 404", got)
	}
}

func TestGiftAPIRateLimitWindowResets(t *testing.T) {
	svc := &Service{
		limiter:       newGiftWindowLimiter(),
		apiRateLimit:  1,
		apiRateWindow: 100 * time.Millisecond,
		logger:        zaptest.NewLogger(t),
	}
	if got := postGiftAPI(svc, "203.0.113.9").Code; got != http.StatusNotFound {
		t.Fatalf("first status = %d, want 404", got)
	}
	if got := postGiftAPI(svc, "203.0.113.9").Code; got != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", got)
	}
	time.Sleep(150 * time.Millisecond)
	if got := postGiftAPI(svc, "203.0.113.9").Code; got != http.StatusNotFound {
		t.Fatalf("post-window status = %d, want 404", got)
	}
}

func TestGiftAPIRateLimitOffWhenUnconfigured(t *testing.T) {
	noLimiter := newGiftRateService(t, nil, 1)
	for i := 0; i < 5; i++ {
		if got := postGiftAPI(noLimiter, "203.0.113.9").Code; got != http.StatusNotFound {
			t.Fatalf("nil limiter call %d status = %d, want 404", i+1, got)
		}
	}

	limiter := newGiftWindowLimiter()
	zeroLimit := newGiftRateService(t, limiter, 0)
	for i := 0; i < 5; i++ {
		if got := postGiftAPI(zeroLimit, "203.0.113.9").Code; got != http.StatusNotFound {
			t.Fatalf("zero limit call %d status = %d, want 404", i+1, got)
		}
	}
	if len(limiter.calls) != 0 {
		t.Fatalf("zero limit still spent budget: %+v", limiter.calls)
	}
}

func TestGiftAPIRateLimitSkipsNonAPIPOSTs(t *testing.T) {
	svc := newGiftRateService(t, newGiftWindowLimiter(), 1)
	if got := postGiftAPI(svc, "203.0.113.9").Code; got != http.StatusNotFound {
		t.Fatalf("first API status = %d, want 404", got)
	}
	if got := postGiftAPI(svc, "203.0.113.9").Code; got != http.StatusTooManyRequests {
		t.Fatalf("second API status = %d, want 429", got)
	}
	req := httptest.NewRequest(http.MethodPost, "/custom-fragment/", nil)
	rec := httptest.NewRecorder()
	svc.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-API POST status = %d, want ungated 404", rec.Code)
	}
}

func TestGiftAPIRateLimitHashesClientKeys(t *testing.T) {
	limiter := newGiftWindowLimiter()
	svc := newGiftRateService(t, limiter, 1)
	postGiftAPI(svc, "203.0.113.9")
	for _, call := range limiter.calls {
		if strings.Contains(call.key, "203.0.113.9") {
			t.Fatalf("limiter key leaked client IP: %q", call.key)
		}
	}
}

func TestGiftAPIRateLimitConcurrent(t *testing.T) {
	const budget = 8
	svc := newGiftRateService(t, newGiftWindowLimiter(), budget)

	const workers = 30
	var wg sync.WaitGroup
	codes := make(chan int, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- postGiftAPI(svc, "203.0.113.9").Code
		}()
	}
	wg.Wait()
	close(codes)
	passed, limited := 0, 0
	for code := range codes {
		if code == http.StatusTooManyRequests {
			limited++
		} else {
			passed++
		}
	}
	if passed != budget || limited != workers-budget {
		t.Fatalf("passed = %d limited = %d, want %d/%d", passed, limited, budget, workers-budget)
	}
}
