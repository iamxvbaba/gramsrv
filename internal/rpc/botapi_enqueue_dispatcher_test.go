package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// TestBotAPIEnqueueDispatcherSynchronousBeforeRun locks in the synchronous fallback before the
// dispatcher is started: in tests/unwired setups, enqueue behaves exactly as the old version
// (the job runs immediately in the caller's goroutine).
func TestBotAPIEnqueueDispatcherSynchronousBeforeRun(t *testing.T) {
	d := newBotAPIEnqueueDispatcher(zaptest.NewLogger(t), 4)
	ran := false
	d.Enqueue(context.Background(), func(context.Context) { ran = true })
	if !ran {
		t.Fatal("job must run synchronously before Run is called")
	}
}

// TestBotAPIEnqueueDispatcherFIFOOrder locks in the single-worker FIFO after start: the update_id
// order for the same bot must match the enqueue order (out-of-order would let the bot side's
// getUpdates see out-of-sequence messages).
func TestBotAPIEnqueueDispatcherFIFOOrder(t *testing.T) {
	d := newBotAPIEnqueueDispatcher(zaptest.NewLogger(t), 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	for !d.started.Load() {
		time.Sleep(time.Millisecond)
	}

	var mu sync.Mutex
	var order []int
	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		i := i
		d.Enqueue(context.Background(), func(context.Context) {
			mu.Lock()
			order = append(order, i)
			if len(order) == 5 {
				close(done)
			}
			mu.Unlock()
		})
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("jobs did not complete")
	}
	mu.Lock()
	defer mu.Unlock()
	for i, got := range order {
		if got != i {
			t.Fatalf("order = %v, want FIFO", order)
		}
	}
}

// TestBotAPIEnqueueDispatcherFallsBackWhenFull locks in the synchronous fallback when the queue
// is full: a Bot API queue row is the delivery source of truth (no getDifference-like fallback),
// so when full the sender waits one extra INSERT and never drops the update.
func TestBotAPIEnqueueDispatcherFallsBackWhenFull(t *testing.T) {
	d := newBotAPIEnqueueDispatcher(zaptest.NewLogger(t), 1)
	d.started.Store(true) // simulate started but the worker does not consume (blocked scenario)

	// Fill the queue of capacity 1.
	d.Enqueue(context.Background(), func(context.Context) {})

	ran := false
	d.Enqueue(context.Background(), func(context.Context) { ran = true })
	if !ran {
		t.Fatal("job must fall back to synchronous execution when queue is full")
	}
}
