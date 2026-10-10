package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type fakeDeletionNotifications struct {
	queue      []domain.AccountDeletionNotification
	nextID     int64
	completed  []int64
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

func (s *fakeDeletionNotifications) CompleteAccountDeletionNotification(_ context.Context, id int64, _ time.Time) error {
	s.completed = append(s.completed, id)
	return nil
}

type offlineViewerUsers struct {
	UsersService
}

func (s *offlineViewerUsers) ByID(_ context.Context, _, userID int64) (domain.User, bool, error) {
	return domain.User{ID: userID, Deleted: true, DeletedAt: 1_800_000_000}, true, nil
}

// The durable queue is what makes a tombstone converge for viewers who were
// offline when the deletion committed: they get updateUser plus the display name
// they would otherwise never learn about.
func TestAccountDeletionNotificationsDispatchToOfflineViewer(t *testing.T) {
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

	if len(notifications.completed) != 1 || notifications.completed[0] != 1 {
		t.Fatalf("completed notifications = %v, want the dispatched row", notifications.completed)
	}
	push, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok {
		t.Fatalf("push = %T", sessions.lastUserPush())
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
		t.Fatalf("tombstone push must carry updateUser, updateUserName and an empty updateUserStatus: %+v", push.Updates)
	}
	if len(push.Users) != 1 {
		t.Fatalf("pushed users = %+v, want one tombstone peer", push.Users)
	}
	peer, ok := push.Users[0].(*tg.User)
	if !ok || peer.ID != deletedUserID || !peer.Deleted || peer.FirstName != deletedAccountDisplayName {
		t.Fatalf("pushed peer = %+v, want the deleted tombstone", push.Users[0])
	}
	if !isEmptyUserStatus(peer.Status) {
		t.Fatalf("pushed tombstone status = %+v, want userStatusEmpty", peer.Status)
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
