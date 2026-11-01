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
// The bot_api_updates row itself is the delivery source of truth (getUpdates reads only that
// table and has no getDifference-style convergence path), so when the queue is full the durable
// insert completes on the caller's path instead of being dropped -- the sender waits for one more
// INSERT in exchange for never losing an update.
//
// Single-worker FIFO: guarantees the update_id order for the same bot matches the send order
// (a concurrent goroutine pool would let the bot side's getUpdates see out-of-order messages).

const (
	defaultBotAPIEnqueueBuffer    = 4096
	botAPIEnqueueJobTimeout       = 10 * time.Second
	botAPIEnqueueCallerPathReason = "bot api enqueue queue full, executing durable insert on caller path"
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

// RunBotAPIEnqueue starts the Bot API enqueue background worker, launched by main together with
// the other dispatchers. It blocks until ctx is cancelled; before it runs, enqueue completes the
// durable insert on the caller's path.
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

// Enqueue submits one Bot API queue write. When the dispatcher has not started it runs on the
// caller's path; once started it goes into the FIFO, and when that is full it falls back to the
// caller's path for the durable insert -- never dropped (the queue row is the delivery source of
// truth).
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
		d.log.Warn(botAPIEnqueueCallerPathReason)
		job(reqCtx)
	}
}
