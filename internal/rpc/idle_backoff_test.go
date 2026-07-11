package rpc

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestIdleBackoffSequenceAndReset(t *testing.T) {
	backoff := newIdleBackoff(100*time.Millisecond, 450*time.Millisecond)
	wantIdle := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		450 * time.Millisecond,
		450 * time.Millisecond,
	}
	for i, want := range wantIdle {
		if got := backoff.IdleDelay(); got != want {
			t.Fatalf("idle delay[%d] = %v, want %v", i, got, want)
		}
	}
	if got := backoff.ActiveDelay(); got != 100*time.Millisecond {
		t.Fatalf("active delay = %v, want base interval", got)
	}
	if got := backoff.IdleDelay(); got != 100*time.Millisecond {
		t.Fatalf("idle delay after reset = %v, want base interval", got)
	}
}

func TestIdleBackoffLoopDrainsActiveWorkImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	done := make(chan struct{})
	started := time.Now()
	go func() {
		defer close(done)
		runIdleBackoffLoop(ctx, time.Second, time.Second, func(context.Context) bool {
			if calls.Add(1) < 4 {
				return true
			}
			cancel()
			return false
		})
	}()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("active drain waited for idle interval")
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("dispatch calls = %d, want 4 consecutive active drains", got)
	}
	if elapsed := time.Since(started); elapsed >= 300*time.Millisecond {
		t.Fatalf("active drain elapsed = %v, want no 1s base delay", elapsed)
	}
}

func TestIdleBackoffSanitizesMaxBelowBase(t *testing.T) {
	backoff := newIdleBackoff(2*time.Second, time.Second)
	if got := backoff.IdleDelay(); got != 2*time.Second {
		t.Fatalf("first idle delay = %v, want base interval", got)
	}
	if got := backoff.IdleDelay(); got != 2*time.Second {
		t.Fatalf("second idle delay = %v, want clamped max at base interval", got)
	}
}
