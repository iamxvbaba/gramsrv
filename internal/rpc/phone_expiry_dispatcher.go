package rpc

import (
	"context"
	"time"

	"go.uber.org/zap"
)

const defaultPhoneExpiryTick = time.Second

// PhoneExpiryDispatcher is the server-side fallback timeout worker for private calls: when both
// sides drop off or hang, it moves the timed-out call to a terminal state, pushes both sides, and
// records "missed call" history. On the normal path the client's own 20s/90s timers discard first,
// and the two idempotently converge on the tombstone.
type PhoneExpiryDispatcher struct {
	router   *Router
	log      *zap.Logger
	interval time.Duration
}

func NewPhoneExpiryDispatcher(router *Router, log *zap.Logger, interval time.Duration) *PhoneExpiryDispatcher {
	if log == nil {
		log = zap.NewNop()
	}
	if interval <= 0 {
		interval = defaultPhoneExpiryTick
	}
	return &PhoneExpiryDispatcher{router: router, log: log, interval: interval}
}

func (d *PhoneExpiryDispatcher) Run(ctx context.Context) {
	if d == nil || d.router == nil {
		return
	}
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.DispatchOnce(ctx)
		}
	}
}

func (d *PhoneExpiryDispatcher) DispatchOnce(ctx context.Context) {
	if d == nil || d.router == nil || d.router.deps.Phone == nil {
		return
	}
	expired := d.router.deps.Phone.ExpireDue(ctx, d.router.clock.Now())
	for _, call := range expired {
		// ctx has no session anchor => the push excludes no device; all devices on both sides
		// receive the terminal state.
		d.router.pushPhoneCallDiscardedBoth(ctx, call)
		d.router.sendPhoneCallServiceMessage(ctx, call)
		d.log.Info("phone call expired",
			zap.Int64("call_id", call.ID),
			zap.String("reason", string(call.DiscardReason)))
	}
}
