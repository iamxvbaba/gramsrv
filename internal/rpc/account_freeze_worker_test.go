package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func TestAccountFreezeNotificationPushesCurrentViewerProjection(t *testing.T) {
	const (
		viewerID = int64(1001)
		frozenID = int64(1002)
	)
	sessions := &captureSessions{}
	freezeSvc := &freezeWorkerService{}
	projectedBase := domain.User{ID: frozenID, FirstName: "Frozen", Deleted: true}
	users := &freezeWorkerUsers{user: projectedBase.DeletedTombstone()}
	r := New(Config{}, Deps{
		AccountFreezeNotifications: freezeSvc,
		Users:                      users,
		Sessions:                   sessions,
	}, zaptest.NewLogger(t), clock.System)

	r.dispatchAccountFreezeNotification(context.Background(), freezeSvc, domain.AccountFreezeNotification{
		ID: 7, TargetUserID: viewerID, FrozenUserID: frozenID, Version: 4, Frozen: true,
	})

	if len(freezeSvc.completed) != 1 || freezeSvc.completed[0] != [2]int64{7, 4} {
		t.Fatalf("completed = %v, want [[7 4]]", freezeSvc.completed)
	}
	if got := sessions.pushedUserIDs(); len(got) != 1 || got[0] != viewerID {
		t.Fatalf("pushed user IDs = %v, want [%d]", got, viewerID)
	}
	updates, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || len(updates.Updates) != 1 || len(updates.Users) != 1 {
		t.Fatalf("push = %#v, want updateUser plus projected user", sessions.lastUserPush())
	}
	if update, ok := updates.Updates[0].(*tg.UpdateUser); !ok || update.UserID != frozenID {
		t.Fatalf("update = %#v, want updateUser(%d)", updates.Updates[0], frozenID)
	}
	projected, ok := updates.Users[0].(*tg.User)
	if !ok || !projected.Deleted {
		t.Fatalf("projected user = %#v, want deleted user (frozen shown as deleted to peers)", updates.Users[0])
	}
	if firstName, _ := projected.GetFirstName(); firstName != "" {
		t.Fatalf("projected user = %#v, want persona stripped", updates.Users[0])
	}
	if username, _ := projected.GetUsername(); username != "" {
		t.Fatalf("projected user = %#v, want persona stripped", updates.Users[0])
	}
}

func TestAccountFreezeNotificationLoadsCurrentStateAndRetriesLoadFailure(t *testing.T) {
	const (
		viewerID = int64(2001)
		frozenID = int64(2002)
	)
	sessions := &captureSessions{}
	freezeSvc := &freezeWorkerService{}
	users := &freezeWorkerUsers{err: errors.New("projection unavailable")}
	r := New(Config{}, Deps{
		AccountFreezeNotifications: freezeSvc,
		Users:                      users,
		Sessions:                   sessions,
	}, zaptest.NewLogger(t), clock.System)
	notification := domain.AccountFreezeNotification{
		ID: 8, TargetUserID: viewerID, FrozenUserID: frozenID, Version: 5, Frozen: true,
	}

	r.dispatchAccountFreezeNotification(context.Background(), freezeSvc, notification)
	if len(freezeSvc.completed) != 0 || len(sessions.pushedUserIDs()) != 0 {
		t.Fatalf("failed load completed=%v pushes=%v, want retry without push", freezeSvc.completed, sessions.pushedUserIDs())
	}

	// The queued payload may say frozen, but delivery must hydrate the latest
	// viewer projection so a newer unfreeze can never be overwritten by stale work.
	users.err = nil
	users.user = domain.User{ID: frozenID, FirstName: "Active"}
	r.dispatchAccountFreezeNotification(context.Background(), freezeSvc, notification)
	updates, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || len(updates.Users) != 1 {
		t.Fatalf("push = %#v", sessions.lastUserPush())
	}
	projected, ok := updates.Users[0].(*tg.User)
	if !ok || projected.Restricted {
		t.Fatalf("latest projected user = %#v, want unrestricted", updates.Users[0])
	}
	if len(freezeSvc.completed) != 1 || freezeSvc.completed[0] != [2]int64{8, 5} {
		t.Fatalf("completed = %v, want [[8 5]]", freezeSvc.completed)
	}
}

type freezeWorkerService struct {
	completed [][2]int64
}

func (*freezeWorkerService) AccountFreeze(context.Context, int64) (domain.AccountFreeze, bool, error) {
	return domain.AccountFreeze{}, false, nil
}

func (*freezeWorkerService) ClaimAccountFreezeNotifications(context.Context, time.Time, int, time.Duration) ([]domain.AccountFreezeNotification, error) {
	return nil, nil
}

func (s *freezeWorkerService) CompleteAccountFreezeNotification(_ context.Context, id, version int64, _ time.Time) error {
	s.completed = append(s.completed, [2]int64{id, version})
	return nil
}

type freezeWorkerUsers struct {
	user domain.User
	err  error
}

func (s *freezeWorkerUsers) Self(context.Context, int64) (domain.User, error) {
	return s.user, s.err
}

func (s *freezeWorkerUsers) ByID(context.Context, int64, int64) (domain.User, bool, error) {
	return s.user, s.err == nil, s.err
}

func (s *freezeWorkerUsers) ByIDs(context.Context, int64, []int64) ([]domain.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	return []domain.User{s.user}, nil
}
