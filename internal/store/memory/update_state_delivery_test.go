package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestUpdateStateDeliveredCommitSeparatesConfirmedAndObserved(t *testing.T) {
	ctx := context.Background()
	store := NewUpdateStateStore()
	authKeyID := [8]byte{1}
	const userID int64 = 1001
	if err := store.ObserveClientState(ctx, authKeyID, userID, domain.UpdateState{Pts: 2, Date: 20}); err != nil {
		t.Fatalf("observe request: %v", err)
	}
	if _, found, err := store.Get(ctx, authKeyID, userID); err != nil || found {
		t.Fatalf("observed request fabricated confirmed state: found=%v err=%v", found, err)
	}
	if err := store.CommitDeliveredState(ctx, authKeyID, userID, domain.UpdateState{Pts: 5, Date: 50}, domain.UpdateStateCommitDeliveredOnly); err != nil {
		t.Fatalf("commit delivered: %v", err)
	}
	confirmed, found, err := store.Get(ctx, authKeyID, userID)
	if err != nil || !found || confirmed.Pts != 5 {
		t.Fatalf("confirmed = %+v/%v err=%v, want pts=5", confirmed, found, err)
	}
	observed, found := store.ObservedClientState(authKeyID, userID)
	if !found || observed.Pts != 2 {
		t.Fatalf("observed = %+v/%v, want pts=2", observed, found)
	}
}

func TestUpdateStateBaselineCommitIsAtomicAndMonotonic(t *testing.T) {
	ctx := context.Background()
	store := NewUpdateStateStore()
	authKeyID := [8]byte{2}
	const userID int64 = 1002
	if err := store.CommitDeliveredState(ctx, authKeyID, userID, domain.UpdateState{Pts: 8, Date: 80}, domain.UpdateStateCommitDeliveredAndObservedBaseline); err != nil {
		t.Fatalf("commit baseline: %v", err)
	}
	if err := store.CommitDeliveredState(ctx, authKeyID, userID, domain.UpdateState{Pts: 3, Date: 30}, domain.UpdateStateCommitDeliveredAndObservedBaseline); err != nil {
		t.Fatalf("commit stale baseline: %v", err)
	}
	confirmed, _, _ := store.Get(ctx, authKeyID, userID)
	observed, _ := store.ObservedClientState(authKeyID, userID)
	if confirmed.Pts != 8 || observed.Pts != 8 {
		t.Fatalf("out-of-order baseline regressed state: confirmed=%+v observed=%+v", confirmed, observed)
	}
}

func TestUpdateStateDeleteAuthKeyRemovesObservedOnlyRows(t *testing.T) {
	ctx := context.Background()
	store := NewUpdateStateStore()
	authKeyID := [8]byte{3}
	if err := store.ObserveClientState(ctx, authKeyID, 1003, domain.UpdateState{Pts: 4}); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if err := store.DeleteAuthKey(ctx, authKeyID); err != nil {
		t.Fatalf("delete auth key: %v", err)
	}
	if _, found := store.ObservedClientState(authKeyID, 1003); found {
		t.Fatal("observed-only row survived auth-key deletion")
	}
}
