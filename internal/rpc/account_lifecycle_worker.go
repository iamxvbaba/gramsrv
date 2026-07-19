package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

type accountLifecycleWorkerService interface {
	SweepDueAccountDeletions(ctx context.Context, now time.Time, limit int) ([]domain.AccountDeletionResult, error)
	ClaimAccountDeletionNotifications(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.AccountDeletionNotification, error)
	CompleteAccountDeletionNotification(ctx context.Context, id int64, now time.Time) error
}

// RunAccountLifecycle executes all due account deletion sources through one
// tombstone path and drains the durable non-pts updateUser queue. The queue is
// a crash-safe, bounded online nudge: offline users are completed after the
// first attempt because getDialogs/getHistory hydration independently returns
// the authoritative tombstone. This avoids an immortal retry queue for a
// non-pts update that cannot participate in getDifference.
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
	for _, result := range results {
		if !result.Changed {
			continue
		}
		r.invalidateRPCProjectionForUser(result.User.ID)
		r.finishDeletedAccountAuthorizations(context.Background(), result.User.ID, result.RevokedAuthorizations)
	}
	if err != nil {
		// SweepDueAccountDeletions may return already-committed results before a
		// later candidate fails. Always finish those sessions/caches and drain
		// their durable notifications; the failed and remaining candidates are
		// retried from their authoritative due rows on the next tick.
		r.log.Warn("account lifecycle deletion sweep partially failed", zap.Int("completed", len(results)), zap.Error(err))
	}
	for {
		claimCtx, claimCancel := context.WithTimeout(ctx, 30*time.Second)
		notifications, err := svc.ClaimAccountDeletionNotifications(claimCtx, now, batch, 2*time.Minute)
		claimCancel()
		if err != nil {
			r.log.Warn("claim account deletion notifications failed", zap.Error(err))
			return
		}
		for _, notification := range notifications {
			r.dispatchAccountDeletionNotification(ctx, svc, notification)
		}
		if len(notifications) < batch {
			return
		}
	}
}

func (r *Router) dispatchAccountDeletionNotification(ctx context.Context, svc accountLifecycleWorkerService, notification domain.AccountDeletionNotification) {
	now := r.clock.Now().UTC()
	updates := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: notification.DeletedUserID}},
		Users: []tg.UserClass{tgUser(domain.User{
			ID:      notification.DeletedUserID,
			Deleted: true,
		})},
		Date: int(now.Unix()),
	}
	r.pushUserUpdates(ctx, notification.TargetUserID, updates)
	if err := svc.CompleteAccountDeletionNotification(ctx, notification.ID, now); err != nil {
		r.log.Warn("complete account deletion notification failed", zap.Int64("notification_id", notification.ID), zap.Error(err))
	}
}
