package account

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestDeleteAccountTwoFADelayDecisionMatrix(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	authKey := [8]byte{1}
	tests := []struct {
		name            string
		hasPassword     bool
		passwordUpdated time.Time
		createdAt       time.Time
		lastSeen        int
		wantKind        domain.AccountDeleteKind
	}{
		{name: "no password deletes immediately", createdAt: now.Add(-time.Hour), lastSeen: int(now.Unix()), wantKind: domain.AccountDeleteImmediate},
		{name: "old password and recent activity delays", hasPassword: true, passwordUpdated: now.Add(-8 * 24 * time.Hour), createdAt: now.Add(-30 * 24 * time.Hour), lastSeen: int(now.Add(-time.Hour).Unix()), wantKind: domain.AccountDeleteDelayed},
		{name: "recent password change deletes immediately", hasPassword: true, passwordUpdated: now.Add(-2 * 24 * time.Hour), createdAt: now.Add(-30 * 24 * time.Hour), lastSeen: int(now.Add(-time.Hour).Unix()), wantKind: domain.AccountDeleteImmediate},
		{name: "inactive account deletes immediately", hasPassword: true, passwordUpdated: now.Add(-30 * 24 * time.Hour), createdAt: now.Add(-30 * 24 * time.Hour), lastSeen: int(now.Add(-8 * 24 * time.Hour).Unix()), wantKind: domain.AccountDeleteImmediate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &fakeAccountLifecycleStore{snapshot: domain.AccountDeletionSnapshot{
				User:        domain.User{ID: 42, Phone: "15550010000", CreatedAt: test.createdAt, LastSeenAt: test.lastSeen},
				HasPassword: test.hasPassword, PasswordUpdatedAt: test.passwordUpdated,
			}}
			svc := NewService(memory.NewPasswordStore(), WithAccountLifecycle(lifecycle))
			outcome, err := svc.DeleteAccount(context.Background(), 42, authKey, "manual", nil, now)
			if err != nil {
				t.Fatalf("DeleteAccount: %v", err)
			}
			if outcome.Kind != test.wantKind {
				t.Fatalf("kind = %q, want %q", outcome.Kind, test.wantKind)
			}
			if test.wantKind == domain.AccountDeleteDelayed {
				if lifecycle.scheduled == nil || !strings.Contains(lifecycle.scheduled.ServiceMessage, "tg://confirmphone?") || outcome.WaitSeconds != int(accountDeletionDelay.Seconds()) {
					t.Fatalf("delayed outcome=%+v scheduled=%+v", outcome, lifecycle.scheduled)
				}
			} else if lifecycle.executedSource == "" {
				t.Fatal("immediate path did not execute the tombstone boundary")
			}
		})
	}
}

func TestConfirmPhoneCancelsPendingDeletionAndRevokesRequester(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	users := memory.NewUserStore()
	u, err := users.Create(ctx, domain.User{Phone: "15550010001", FirstName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	requester := [8]byte{9}
	confirming := [8]byte{8}
	lifecycle := &fakeAccountLifecycleStore{snapshot: domain.AccountDeletionSnapshot{User: u}}
	svc := NewService(memory.NewPasswordStore(),
		WithUsers(users),
		WithPhoneChange(nil, nil, memory.NewCodeStore(), nil, "12345", 5*time.Minute, 5),
		WithAccountLifecycle(lifecycle),
	)
	rawToken, digest, err := newAccountDeletionToken()
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.pending = &domain.AccountDeletionRequest{
		ID: 1, UserID: u.ID, RequesterAuthKeyID: requester, State: domain.AccountDeletionPending,
		ConfirmHashDigest: digest, RequestedAt: now, ExecuteAt: now.Add(accountDeletionDelay),
	}
	hash, delivery, err := svc.SendConfirmPhoneCode(ctx, u.ID, confirming, 77, rawToken)
	if err != nil || hash == "" || delivery.Length != 5 {
		t.Fatalf("SendConfirmPhoneCode hash=%q delivery=%+v err=%v", hash, delivery, err)
	}
	revoked, err := svc.ConfirmPhone(ctx, u.ID, confirming, hash, "12345", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ConfirmPhone: %v", err)
	}
	if len(revoked) != 1 || revoked[0].AuthKeyID != requester || lifecycle.pending != nil {
		t.Fatalf("revoked=%+v pending=%+v", revoked, lifecycle.pending)
	}
	if _, err := svc.ConfirmPhone(ctx, u.ID, confirming, hash, "12345", now.Add(2*time.Minute)); !errors.Is(err, domain.ErrPhoneCodeExpired) {
		t.Fatalf("replay error = %v, want expired", err)
	}
}

type fakeAccountLifecycleStore struct {
	snapshot       domain.AccountDeletionSnapshot
	pending        *domain.AccountDeletionRequest
	scheduled      *domain.ScheduleAccountDeletion
	executedSource domain.AccountDeletionSource
}

func (f *fakeAccountLifecycleStore) AccountDeletionSnapshot(context.Context, int64) (domain.AccountDeletionSnapshot, bool, error) {
	f.snapshot.Pending = f.pending
	return f.snapshot, true, nil
}

func (f *fakeAccountLifecycleStore) ScheduleAccountDeletion(_ context.Context, req domain.ScheduleAccountDeletion) (domain.AccountDeletionRequest, bool, error) {
	f.scheduled = &req
	pending := domain.AccountDeletionRequest{ID: 1, UserID: req.UserID, RequesterAuthKeyID: req.RequesterAuthKeyID, State: domain.AccountDeletionPending, Reason: req.Reason, ConfirmHashDigest: req.ConfirmHashDigest, RequestedAt: req.RequestedAt, ExecuteAt: req.ExecuteAt}
	f.pending = &pending
	return pending, true, nil
}

func (f *fakeAccountLifecycleStore) PendingAccountDeletionByHash(_ context.Context, userID int64, digest [32]byte) (domain.AccountDeletionRequest, bool, error) {
	if f.pending == nil || f.pending.UserID != userID || f.pending.ConfirmHashDigest != digest {
		return domain.AccountDeletionRequest{}, false, nil
	}
	return *f.pending, true, nil
}

func (f *fakeAccountLifecycleStore) ExecuteAccountDeletion(_ context.Context, userID int64, source domain.AccountDeletionSource, reason string, now time.Time) (domain.AccountDeletionResult, error) {
	f.executedSource = source
	u := f.snapshot.User
	u.Deleted = true
	u.DeletedAt = now.Unix()
	u.DeletionSource = source
	u.DeletionReason = reason
	u = u.DeletedTombstone()
	return domain.AccountDeletionResult{User: u, Changed: true}, nil
}

func (f *fakeAccountLifecycleStore) CancelAccountDeletion(_ context.Context, userID int64, digest [32]byte, _ time.Time) ([]domain.Authorization, error) {
	if f.pending == nil || f.pending.UserID != userID || f.pending.ConfirmHashDigest != digest {
		return nil, domain.ErrAccountDeletionHashInvalid
	}
	revoked := []domain.Authorization{{AuthKeyID: f.pending.RequesterAuthKeyID, UserID: userID}}
	f.pending = nil
	return revoked, nil
}

func (*fakeAccountLifecycleStore) DueAccountDeletions(context.Context, time.Time, int) ([]domain.AccountDeletionCandidate, error) {
	return nil, nil
}
func (*fakeAccountLifecycleStore) ClaimAccountDeletionNotifications(context.Context, time.Time, int, time.Duration) ([]domain.AccountDeletionNotification, error) {
	return nil, nil
}
func (*fakeAccountLifecycleStore) CompleteAccountDeletionNotification(context.Context, int64, time.Time) error {
	return nil
}
