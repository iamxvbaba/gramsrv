package rpc

import (
	"context"
	"time"

	"go.uber.org/zap"
)

const defaultCollectibleDropTick = 30 * time.Second

// collectibleDropActivator opens scheduled collectible pools that are due.
type collectibleDropActivator interface {
	ActivateDueCollectibleRevisions(ctx context.Context) ([]int64, error)
}

// CollectibleDropDispatcher opens deferred collectible pools: an operator can
// publish a pool ahead of time with a future go-live instant, and this loop
// activates it once that instant arrives. The plain gift is unaffected.
type CollectibleDropDispatcher struct {
	gifts    collectibleDropActivator
	log      *zap.Logger
	interval time.Duration
}

func NewCollectibleDropDispatcher(gifts collectibleDropActivator, log *zap.Logger, interval time.Duration) *CollectibleDropDispatcher {
	if log == nil {
		log = zap.NewNop()
	}
	if interval <= 0 {
		interval = defaultCollectibleDropTick
	}
	return &CollectibleDropDispatcher{gifts: gifts, log: log, interval: interval}
}

func (d *CollectibleDropDispatcher) Run(ctx context.Context) {
	if d == nil || d.gifts == nil {
		return
	}
	d.DispatchOnce(ctx)
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

func (d *CollectibleDropDispatcher) DispatchOnce(ctx context.Context) {
	if d == nil || d.gifts == nil {
		return
	}
	giftIDs, err := d.gifts.ActivateDueCollectibleRevisions(ctx)
	if err != nil {
		d.log.Error("activate due collectible revisions", zap.Error(err))
		return
	}
	for _, giftID := range giftIDs {
		d.log.Info("collectible drop activated", zap.Int64("gift_id", giftID))
	}
}
