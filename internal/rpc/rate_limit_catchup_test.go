package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"
)

// TestCheckCatchupRateLimit 锁定 Phase 2 difference 类 catch-up FLOOD_WAIT 行为：配置阈值后
// 放行返回 nil、超限返回 FLOOD_WAIT 错误；阈值 <=0 或 Limiter 未装配时完全不限速（行为不变）。
func TestCheckCatchupRateLimit(t *testing.T) {
	const userID = int64(7001)

	t.Run("disabled when limit unset", func(t *testing.T) {
		lim := &captureRateLimiter{}
		r := New(Config{}, Deps{Limiter: lim}, zaptest.NewLogger(t), clock.System)
		if err := r.checkCatchupRateLimit(context.Background(), userID, channelDifferenceRateLimitKeyPrefix); err != nil {
			t.Fatalf("disabled limit should allow: %v", err)
		}
		if len(lim.calls) != 0 {
			t.Fatalf("limiter should not be consulted when CatchupRateLimit<=0, calls=%v", lim.calls)
		}
	})

	t.Run("disabled when limiter nil", func(t *testing.T) {
		r := New(Config{CatchupRateLimit: 5}, Deps{}, zaptest.NewLogger(t), clock.System)
		if err := r.checkCatchupRateLimit(context.Background(), userID, peerDialogsRateLimitKeyPrefix); err != nil {
			t.Fatalf("nil limiter should allow: %v", err)
		}
	})

	t.Run("allowed under budget", func(t *testing.T) {
		lim := &captureRateLimiter{}
		r := New(Config{CatchupRateLimit: 5, CatchupRateWindow: time.Minute}, Deps{Limiter: lim}, zaptest.NewLogger(t), clock.System)
		if err := r.checkCatchupRateLimit(context.Background(), userID, channelDifferenceRateLimitKeyPrefix); err != nil {
			t.Fatalf("under budget should allow: %v", err)
		}
		if len(lim.calls) != 1 || lim.calls[0].limit != 5 || lim.calls[0].window != time.Minute || lim.calls[0].cost != 1 {
			t.Fatalf("limiter call = %+v, want limit5/window1m/cost1", lim.calls)
		}
		if lim.calls[0].key != channelDifferenceRateLimitKeyPrefix+"7001" {
			t.Fatalf("limiter key = %q, want difference-prefixed per user", lim.calls[0].key)
		}
	})

	t.Run("flood wait when over budget", func(t *testing.T) {
		lim := &captureRateLimiter{block: true, retryAfter: 7}
		r := New(Config{CatchupRateLimit: 5}, Deps{Limiter: lim}, zaptest.NewLogger(t), clock.System)
		err := r.checkCatchupRateLimit(context.Background(), userID, peerDialogsRateLimitKeyPrefix)
		if err == nil {
			t.Fatal("over budget should return FLOOD_WAIT error")
		}
		// 默认 window 在未配置时回落 defaultCatchupRateWindow。
		if lim.calls[0].window != defaultCatchupRateWindow {
			t.Fatalf("window = %v, want default %v when unset", lim.calls[0].window, defaultCatchupRateWindow)
		}
	})

	t.Run("separate budgets per kind", func(t *testing.T) {
		lim := &captureRateLimiter{}
		r := New(Config{CatchupRateLimit: 5}, Deps{Limiter: lim}, zaptest.NewLogger(t), clock.System)
		_ = r.checkCatchupRateLimit(context.Background(), userID, channelDifferenceRateLimitKeyPrefix)
		_ = r.checkCatchupRateLimit(context.Background(), userID, peerDialogsRateLimitKeyPrefix)
		if len(lim.calls) != 2 || lim.calls[0].key == lim.calls[1].key {
			t.Fatalf("difference and peerDialogs must use distinct keys: %+v", lim.calls)
		}
	})
}
