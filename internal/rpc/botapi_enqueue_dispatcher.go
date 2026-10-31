package rpc

import (
	"context"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Bot API private-chat enqueue async-ification (performance audit H2): when a user->bot private
// chat is sent/edited, the bot_api_updates INSERT (and the possibly-PG-hitting bot check) is
// moved off the sender's RPC synchronous path, so the sender no longer waits an extra PG round
// trip for the Bot API queue write.
//
// Key difference from the channel fanout dispatcher: the bot_api_updates row itself is the
// delivery source of truth (getUpdates reads only that table, with no getDifference-like
// fallback), so when the queue is full it **falls back to running synchronously** rather than
// dropping -- the sender waits one extra INSERT in exchange for not losing the update.
//
// Single-worker FIFO: guarantees the update_id order for the same bot matches the send order
// (a concurrent goroutine pool would let the bot side's getUpdates see out-of-order messages).

const (
	defaultBotAPIEnqueueBuffer  = 4096
	botAPIEnqueueJobTimeout     = 10 * time.Second
	botAPIEnqueueFallbackReason = "bot api enqueue queue full, falling back to synchronous insert"
)

type botAPIEnqueueDispatcher struct {
	log     *zap.Logger
	jobs    chan func(context.Context)
	started atomic.Bool
}

func newBotAPIEnqueueDispatcher(log *zap.Logger, buffer int) *botAPIEnqueueDispatcher {
	if buffer <= 0 {
		buffer = defaultBotAPIEnqueueBuffer
	}
	return &botAPIEnqueueDispatcher{
		log:  log.Named("botapi-enqueue"),
		jobs: make(chan func(context.Context), buffer),
	}
}

// RunBotAPIEnqueue starts the Bot API enqueue background worker, launched alongside the other
// dispatchers by main. Blocks until ctx is canceled; before it is called, enqueue runs
// synchronously (same behavior as the old version, unchanged for tests/unwired setups).
func (r *Router) RunBotAPIEnqueue(ctx context.Context) {
	r.botAPIEnqueueQueue.Run(ctx)
}

func (d *botAPIEnqueueDispatcher) Run(ctx context.Context) {
	if d == nil || !d.started.CompareAndSwap(false, true) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-d.jobs:
			jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), botAPIEnqueueJobTimeout)
			job(jobCtx)
			cancel()
		}
	}
}

// Enqueue submits a Bot API queue-write task. When the dispatcher is not started it runs
// synchronously (using the request ctx); when started it is put into the FIFO, and falls back to
// synchronous execution when full -- it never drops (a queue row is the delivery source of truth).
func (d *botAPIEnqueueDispatcher) Enqueue(reqCtx context.Context, job func(context.Context)) {
	if d == nil || job == nil {
		return
	}
	if !d.started.Load() {
		job(reqCtx)
		return
	}
	select {
	case d.jobs <- job:
	default:
		d.log.Warn(botAPIEnqueueFallbackReason)
		job(reqCtx)
	}
}
