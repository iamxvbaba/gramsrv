package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// TestBotAPIEnqueueDispatcherCallerPathBeforeRun pins the caller-path durable insert before the
// worker starts: bot_api_updates is the delivery source of truth, so a row must not be lost just
// because the background worker has not started.
func TestBotAPIEnqueueDispatcherCallerPathBeforeRun(t *testing.T) {
	d := newBotAPIEnqueueDispatcher(zaptest.NewLogger(t), 4)
	ran := false
	d.Enqueue(context.Background(), func(context.Context) { ran = true })
	if !ran {
		t.Fatal("job must run on caller path before Run is called")
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

// TestBotAPIEnqueueDispatcherUsesCallerPathWhenFull pins the caller-path durable insert when the
// queue is full: the Bot API queue row is the delivery source of truth, so when full the sender
// waits for one more INSERT and nothing is dropped.
func TestBotAPIEnqueueDispatcherUsesCallerPathWhenFull(t *testing.T) {
	d := newBotAPIEnqueueDispatcher(zaptest.NewLogger(t), 1)
	d.started.Store(true) // simulate started but the worker does not consume (blocked scenario)

	// Fill the queue of capacity 1.
	d.Enqueue(context.Background(), func(context.Context) {})

	ran := false
	d.Enqueue(context.Background(), func(context.Context) { ran = true })
	if !ran {
		t.Fatal("job must execute on caller path when queue is full")
	}
}
