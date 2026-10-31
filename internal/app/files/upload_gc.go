package files

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// UploadPartGCWorker periodically cleans up expired, unassembled upload parts.
type UploadPartGCWorker struct {
	files    *Service
	logger   *zap.Logger
	ttl      time.Duration
	interval time.Duration
	batch    int
}

func NewUploadPartGCWorker(files *Service, logger *zap.Logger, ttl, interval time.Duration, batch int) *UploadPartGCWorker {
	if logger == nil {
		logger = zap.NewNop()
	}
	if ttl <= 0 {
		ttl = DefaultUploadPartTTL
	}
	if interval <= 0 {
		interval = DefaultUploadPartGCInterval
	}
	if batch <= 0 {
		batch = DefaultUploadPartGCBatch
	}
	return &UploadPartGCWorker{
		files:    files,
		logger:   logger,
		ttl:      ttl,
		interval: interval,
		batch:    batch,
	}
}

func (w *UploadPartGCWorker) Run(ctx context.Context) {
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

func (w *UploadPartGCWorker) runOnce(ctx context.Context) {
	if w.files == nil {
		return
	}
	deleted, err := w.files.DeleteExpiredUploadParts(ctx, time.Now().Add(-w.ttl), w.batch)
	if err != nil {
		w.logger.Warn("failed to clean up expired upload_parts", zap.Error(err))
		return
	}
	if deleted > 0 {
		w.logger.Info("cleaned up expired upload_parts", zap.Int64("deleted", deleted))
	}
}
