package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

type accountFreezeNotificationService interface {
	ClaimAccountFreezeNotifications(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.AccountFreezeNotification, error)
	CommitAccountFreezeNotificationDelivery(ctx context.Context, id, version int64, payload []byte, now time.Time) error
}

// RunAccountFreezeNotifications drains the crash-safe, coalesced non-pts
// updateUser queue. One attempt is enough for online delivery; offline clients
// recover the current state from viewer-scoped user hydration.
func (r *Router) RunAccountFreezeNotifications(ctx context.Context, interval time.Duration, batch int) {
	if interval <= 0 {
		interval = time.Minute
	}
	if batch <= 0 {
		batch = 500
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		r.drainAccountFreezeNotifications(ctx, batch)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.accountFreezeWake:
		}
	}
}

func (r *Router) drainAccountFreezeNotifications(ctx context.Context, batch int) {
	svc := r.deps.AccountFreezeNotifications
	if svc == nil || r.deps.Users == nil {
		return
	}
	for {
		now := r.clock.Now().UTC()
		claimCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		notifications, err := svc.ClaimAccountFreezeNotifications(claimCtx, now, batch, 2*time.Minute)
		cancel()
		if err != nil {
			r.log.Warn("claim account freeze notifications failed", zap.Error(err))
			return
		}
		for _, notification := range notifications {
			r.dispatchAccountFreezeNotification(ctx, svc, notification)
		}
		if len(notifications) < batch {
			return
		}
	}
}

func (r *Router) dispatchAccountFreezeNotification(ctx context.Context, svc AccountFreezeNotificationService, notification domain.AccountFreezeNotification) {
	r.applyFrozenPresence(notification.FrozenUserID, notification.Frozen)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: notification.FrozenUserID}
	if contacts, ok := r.deps.Contacts.(interface{ InvalidateViewers(...int64) }); ok {
		contacts.InvalidateViewers(notification.TargetUserID)
	}
	if dialogs, ok := r.deps.Dialogs.(interface {
		InvalidateDialog(int64, domain.Peer)
	}); ok {
		dialogs.InvalidateDialog(notification.TargetUserID, peer)
	}
	r.invalidateRPCProjectionForPeer(notification.TargetUserID, peer)

	loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	user, found, err := r.deps.Users.ByID(loadCtx, notification.TargetUserID, notification.FrozenUserID)
	cancel()
	if err != nil {
		r.log.Warn("load frozen user projection for notification failed",
			zap.Int64("target_user_id", notification.TargetUserID),
			zap.Int64("frozen_user_id", notification.FrozenUserID),
			zap.Int64("version", notification.Version),
			zap.Error(err))
		return
	}
	if !found {
		user = domain.User{ID: notification.FrozenUserID, Deleted: true}
	}
	updates := []tg.UpdateClass{&tg.UpdateUser{UserID: notification.FrozenUserID}}
	if notification.Frozen {
		// A frozen account must not keep showing a live presence to its
		// audience; the empty status rides the same durable delivery.
		updates = append(updates, &tg.UpdateUserStatus{
			UserID: notification.FrozenUserID,
			Status: &tg.UserStatusEmpty{},
		})
	}
	payload, err := encodeDeliveryUpdate(&tg.Updates{
		Updates: updates,
		Users:   r.tgUsersForViewer(notification.TargetUserID, []domain.User{user}),
		Date:    int(r.clock.Now().Unix()),
	})
	if err != nil {
		r.log.Warn("encode account freeze notification failed",
			zap.Int64("notification_id", notification.ID),
			zap.Int64("version", notification.Version),
			zap.Error(err))
		return
	}

	commitCtx, commitCancel := context.WithTimeout(ctx, 10*time.Second)
	err = svc.CommitAccountFreezeNotificationDelivery(commitCtx, notification.ID, notification.Version, payload, r.clock.Now().UTC())
	commitCancel()
	if err != nil {
		r.log.Warn("commit account freeze notification delivery failed",
			zap.Int64("notification_id", notification.ID),
			zap.Int64("version", notification.Version),
			zap.Error(err))
	}
}
