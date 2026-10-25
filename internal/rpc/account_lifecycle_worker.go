package rpc

import (
	"context"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

type accountLifecycleWorkerService interface {
	SweepDueAccountDeletions(ctx context.Context, now time.Time, limit int) ([]domain.AccountDeletionResult, error)
}

// RunAccountLifecycle executes all due account deletion sources through one
// tombstone path. Deleted-user projections converge from authoritative reads;
// updateUser is non-PTS and therefore is not queued as a correctness signal.
func (r *Router) RunAccountLifecycle(ctx context.Context, interval time.Duration, batch int) {
	if interval <= 0 {
		interval = time.Minute
	}
	if batch <= 0 {
		batch = 500
	}
	r.runAccountLifecycleOnce(ctx, batch)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runAccountLifecycleOnce(ctx, batch)
		}
	}
}

func (r *Router) runAccountLifecycleOnce(ctx context.Context, batch int) {
	svc, ok := r.deps.Account.(accountLifecycleWorkerService)
	if !ok {
		return
	}
	now := r.clock.Now().UTC()
	sweepCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	results, err := svc.SweepDueAccountDeletions(sweepCtx, now, batch)
	cancel()
	changed := false
	for _, result := range results {
		if !result.Changed {
			continue
		}
		changed = true
		r.log.Info("account deleted",
			zap.Int64("user_id", result.User.ID),
			zap.String("deletion_source", string(result.User.DeletionSource)),
			zap.Int("revoked_authorizations", len(result.RevokedAuthorizations)),
			// NFT usernames that went back to storage instead of dying with the
			// account; the assets stay reissuable while the name stops resolving.
			zap.Int("vaulted_collectible_usernames", result.VaultedCollectibleUsernames))
		r.wakeAccountDeletionNotifications()
		if finishErr := r.finishDeletedAccountAuthorizations(context.Background(), result.User.ID, result.RevokedAuthorizations); finishErr != nil {
			r.log.Error("account lifecycle authorization teardown unavailable", zap.Int64("user_id", result.User.ID), zap.Error(finishErr))
		}
	}
	if changed {
		// One flush covers the entire due batch. Per-user predicate invalidation
		// would scan the same large projection maps four times for every account.
		r.flushRPCProjectionCache()
	}
	if err != nil {
		// SweepDueAccountDeletions may return already-committed results before a
		// later candidate fails. Always finish those sessions/caches; the failed
		// and remaining candidates are retried from their authoritative due rows
		// on the next tick.
		r.log.Warn("account lifecycle deletion sweep partially failed", zap.Int("completed", len(results)), zap.Error(err))
	}
}
