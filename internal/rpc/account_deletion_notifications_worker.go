package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// RunAccountDeletionNotifications moves queued tombstones into the durable
// delivery outbox. Authoritative tombstone reads still provide offline
// convergence; the outbox row is not a client ACK.
func (r *Router) RunAccountDeletionNotifications(ctx context.Context, interval time.Duration, batch int) {
	if interval <= 0 {
		interval = time.Minute
	}
	if batch <= 0 {
		batch = 500
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		r.drainAccountDeletionNotifications(ctx, batch)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.accountDeletionWake:
		}
	}
}

// wakeAccountDeletionNotifications nudges the queue worker so a just-committed
// deletion does not wait for the next tick to reach its first viewers.
func (r *Router) wakeAccountDeletionNotifications() {
	if r == nil || r.accountDeletionWake == nil {
		return
	}
	select {
	case r.accountDeletionWake <- struct{}{}:
	default:
	}
}

func (r *Router) drainAccountDeletionNotifications(ctx context.Context, batch int) {
	svc := r.deps.AccountDeletionNotifications
	if svc == nil || r.deps.Users == nil {
		return
	}
	for {
		now := r.clock.Now().UTC()
		claimCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		notifications, err := svc.ClaimAccountDeletionNotifications(claimCtx, now, batch, 2*time.Minute)
		cancel()
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

func (r *Router) dispatchAccountDeletionNotification(ctx context.Context, svc AccountDeletionNotificationService, notification domain.AccountDeletionNotification) {
	if r == nil || notification.TargetUserID == 0 || notification.DeletedUserID == 0 {
		return
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: notification.DeletedUserID}
	// Drop the viewer's viewer-scoped overlays first: an expired dialog-list hash
	// or a warm contact projection would otherwise re-serve the old persona the
	// next time the client refreshes.
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
	user, found, err := r.deps.Users.ByID(loadCtx, notification.TargetUserID, notification.DeletedUserID)
	cancel()
	if err != nil {
		r.log.Warn("load deleted user projection for notification failed",
			zap.Int64("target_user_id", notification.TargetUserID),
			zap.Int64("deleted_user_id", notification.DeletedUserID),
			zap.Error(err))
		return
	}
	if !found {
		user = domain.User{ID: notification.DeletedUserID, Deleted: true}
	}
	// updateUser alone refreshes only the photo on several clients, so the display
	// name travels in updateUserName next to it. Without that pair the viewer
	// keeps a live composer against an account that no longer exists.
	payload, err := encodeDeliveryUpdate(&tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateUser{UserID: notification.DeletedUserID},
			&tg.UpdateUserName{
				UserID:    notification.DeletedUserID,
				FirstName: deletedAccountDisplayName,
			},
			// Presence is cleared explicitly. Without it the client keeps the
			// cached last-seen and the account reads as "last seen recently"
			// moments after it appeared as deleted -- the same reason a freeze
			// announces an explicit offline status.
			&tg.UpdateUserStatus{
				UserID: notification.DeletedUserID,
				Status: &tg.UserStatusEmpty{},
			},
		},
		Users: r.tgUsersForViewer(notification.TargetUserID, []domain.User{user}),
		Date:  int(r.clock.Now().Unix()),
	})
	if err != nil {
		r.log.Warn("encode account deletion notification failed",
			zap.Int64("notification_id", notification.ID), zap.Error(err))
		return
	}
	// v2: the delivered mark and the viewer's durable outbox row commit together;
	// Egress delivers to online sessions and offline ones reload the tombstone.
	deliverCtx, deliverCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := svc.DeliverAccountDeletionNotification(deliverCtx, notification.ID, r.clock.Now().UTC(), store.DeliveryOutboxEnqueue{
		TargetUserID: notification.TargetUserID, Payload: payload,
		RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
	}); err != nil {
		r.log.Warn("deliver account deletion notification failed",
			zap.Int64("notification_id", notification.ID),
			zap.Error(err))
	}
	deliverCancel()
}
