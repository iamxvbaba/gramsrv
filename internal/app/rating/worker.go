package rating

import (
	"context"
	"time"

	"go.uber.org/zap"
)

const (
	// defaultRecomputeInterval matches the shipped
	// TELESRV_RATING_RECOMPUTE_INTERVAL default.
	defaultRecomputeInterval = 15 * time.Minute
)

// RecomputeWorker keeps the rating read model fresh.
//
// The projection is derived from signals that change outside the rating write
// path (Stars flow, message activity, moderation decisions, account age), so no
// single writer can keep it current. This worker walks the stale projections in
// bounded batches; it never recomputes the whole table in one pass, and a
// cancelled context stops it between users rather than mid-write.
type RecomputeWorker struct {
	service  *Service
	logger   *zap.Logger
	interval time.Duration
	batch    int
}

// NewRecomputeWorker creates the periodic recompute worker. Non-positive
// interval/batch fall back to the shipped defaults, matching the retention
// worker's contract.
func NewRecomputeWorker(service *Service, logger *zap.Logger, interval time.Duration, batch int) *RecomputeWorker {
	if logger == nil {
		logger = zap.NewNop()
	}
	if interval <= 0 {
		interval = defaultRecomputeInterval
	}
	if batch <= 0 {
		batch = defaultRecomputeBatch
	}
	return &RecomputeWorker{service: service, logger: logger, interval: interval, batch: batch}
}

// Run recomputes one batch immediately and then on every tick until ctx is
// done. A disabled or store-less service exits immediately with one explicit
// log line instead of ticking forever over a no-op.
func (w *RecomputeWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	if !w.service.Ready() {
		w.logger.Info("account rating recompute worker disabled",
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

func (w *RecomputeWorker) runOnce(ctx context.Context) {
	if w == nil || w.service == nil {
		return
	}
	processed, err := w.service.RunRecomputeCycle(ctx, w.batch)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		w.logger.Warn("account rating recompute cycle failed",
			zap.Int("processed", processed),
			zap.Int("batch", w.batch),
			zap.Error(err))
		return
	}
	if processed > 0 {
		w.logger.Info("account rating recompute cycle completed",
			zap.Int("processed", processed),
			zap.Int("batch", w.batch))
	}
}
