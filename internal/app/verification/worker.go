package verification

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// defaultNotifyInterval matches the shipped
// TELESRV_VERIFICATION_NOTIFY_INTERVAL default.
const defaultNotifyInterval = 15 * time.Second

// NotificationWorker drains the applicant-notification outbox.
//
// A decision is committed together with its outbox row, never with a message
// send: @verifybot may be blocked, the applicant may be deleted, and the panel
// must not wait on either. Delivery is therefore a separate, retrying cycle over
// durable rows, and this worker is only its cadence.
type NotificationWorker struct {
	service  *Service
	logger   *zap.Logger
	interval time.Duration
	batch    int
}

// NewNotificationWorker creates the periodic delivery worker. Non-positive
// interval/batch fall back to the shipped defaults, matching the rating
// recompute worker's contract.
func NewNotificationWorker(service *Service, logger *zap.Logger, interval time.Duration, batch int) *NotificationWorker {
	if logger == nil {
		logger = zap.NewNop()
	}
	if interval <= 0 {
		interval = defaultNotifyInterval
	}
	if batch <= 0 {
		batch = defaultNotifyBatch
	}
	return &NotificationWorker{service: service, logger: logger, interval: interval, batch: batch}
}

// Run delivers one batch immediately and then on every tick until ctx is done. A
// disabled or store-less service exits immediately with one explicit log line
// instead of ticking forever over a no-op.
func (w *NotificationWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	if !w.service.Ready() {
		w.logger.Info("verification notification worker disabled",
			zap.Bool("enabled", w.service.Enabled()))
		return
	}
	w.runOnce(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *NotificationWorker) runOnce(ctx context.Context) {
	if w == nil || w.service == nil {
		return
	}
	delivered, err := w.service.RunNotificationCycle(ctx, w.batch)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		w.logger.Warn("verification notification cycle failed",
			zap.Int("delivered", delivered),
			zap.Int("batch", w.batch),
			zap.Error(err))
		return
	}
	if delivered > 0 {
		w.logger.Info("verification notification cycle completed",
			zap.Int("delivered", delivered),
			zap.Int("batch", w.batch))
	}
}
