package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type fakeDeletionNotifications struct {
	queue      []domain.AccountDeletionNotification
	nextID     int64
	completed  []int64
	deliveries []store.DeliveryOutboxEnqueue
	claimCalls int
}

func (s *fakeDeletionNotifications) ClaimAccountDeletionNotifications(_ context.Context, _ time.Time, limit int, _ time.Duration) ([]domain.AccountDeletionNotification, error) {
	s.claimCalls++
	out := s.queue
	s.queue = nil
	if len(out) > limit {
		s.queue = out[limit:]
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeDeletionNotifications) DeliverAccountDeletionNotification(_ context.Context, id int64, _ time.Time, delivery store.DeliveryOutboxEnqueue) error {
	s.completed = append(s.completed, id)
	s.deliveries = append(s.deliveries, delivery)
	return nil
}

type offlineViewerUsers struct {
	UsersService
}

func (s *offlineViewerUsers) ByID(_ context.Context, _, userID int64) (domain.User, bool, error) {
	return domain.User{ID: userID, Deleted: true, DeletedAt: 1_800_000_000}, true, nil
}

// v2: each claimed row becomes one durable delivery carrying the complete
// tombstone projection; Core never pushes to sessions itself.
func TestAccountDeletionNotificationsEnqueueDurableDelivery(t *testing.T) {
	const (
		targetUserID  = int64(5005)
		deletedUserID = int64(6006)
	)
	notifications := &fakeDeletionNotifications{queue: []domain.AccountDeletionNotification{{
		ID: 1, TargetUserID: targetUserID, DeletedUserID: deletedUserID,
	}}}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:                        &offlineViewerUsers{},
		Sessions:                     sessions,
		AccountDeletionNotifications: notifications,
	}, zaptest.NewLogger(t), clock.System)

	r.drainAccountDeletionNotifications(context.Background(), 10)

	if len(notifications.completed) != 1 || notifications.completed[0] != 1 || len(notifications.deliveries) != 1 {
		t.Fatalf("delivered notifications = %v, want the dispatched row", notifications.completed)
	}
	if sessions.lastUserPush() != nil {
		t.Fatalf("Core pushed directly: %+v", sessions.lastUserPush())
	}
	delivery := notifications.deliveries[0]
	if delivery.TargetUserID != targetUserID || delivery.RecoveryPolicy != store.OutboxRecoveryAbsoluteReload {
		t.Fatalf("delivery = %+v", delivery)
	}
	decoded, err := decodeDeliveryUpdate(delivery.Payload)
	if err != nil {
		t.Fatalf("decode delivery: %v", err)
	}
	push, ok := decoded.(*tg.Updates)
	if !ok {
		t.Fatalf("delivery = %T", decoded)
	}
	var sawRefresh, sawName, sawStatus bool
	for _, update := range push.Updates {
		switch typed := update.(type) {
		case *tg.UpdateUser:
			sawRefresh = typed.UserID == deletedUserID
		case *tg.UpdateUserName:
			sawName = typed.UserID == deletedUserID && typed.FirstName == deletedAccountDisplayName
		case *tg.UpdateUserStatus:
			// Without an explicit empty status the client keeps its cached
			// last-seen and the deleted account reads as recently online.
			_, empty := typed.Status.(*tg.UserStatusEmpty)
			sawStatus = typed.UserID == deletedUserID && empty
		}
	}
	if !sawRefresh || !sawName || !sawStatus {
		t.Fatalf("tombstone delivery must carry updateUser, updateUserName and an empty updateUserStatus: %+v", push.Updates)
	}
	if len(push.Users) != 1 {
		t.Fatalf("delivered users = %+v, want one tombstone peer", push.Users)
	}
	peer, ok := push.Users[0].(*tg.User)
	if !ok || peer.ID != deletedUserID || !peer.Deleted || peer.FirstName != deletedAccountDisplayName {
		t.Fatalf("delivered peer = %+v, want the deleted tombstone", push.Users[0])
	}
	if !isEmptyUserStatus(peer.Status) {
		t.Fatalf("delivered tombstone status = %+v, want userStatusEmpty", peer.Status)
	}
}

// An empty queue must not spin: the drain loop stops as soon as a claim comes
// back short.
func TestAccountDeletionNotificationsDrainStopsOnEmptyQueue(t *testing.T) {
	notifications := &fakeDeletionNotifications{}
	r := New(Config{}, Deps{
		Users:                        &offlineViewerUsers{},
		Sessions:                     &captureSessions{},
		AccountDeletionNotifications: notifications,
	}, zaptest.NewLogger(t), clock.System)

	r.drainAccountDeletionNotifications(context.Background(), 10)

	if notifications.claimCalls != 1 {
		t.Fatalf("claim calls = %d, want exactly one for an empty queue", notifications.claimCalls)
	}
}
