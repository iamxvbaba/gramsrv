package memory

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestBootstrapUpdateJobRetriesBeforeFailed(t *testing.T) {
	ctx := context.Background()
	store := NewBootstrapUpdateJobStore()
	job, err := store.EnqueueLoginMessage(ctx, domain.BootstrapUpdateJob{
		Kind:         domain.BootstrapUpdateJobLoginMessage,
		UserID:       1000000001,
		AuthKeyID:    [8]byte{1, 2, 3},
		SessionID:    77,
		MessageBoxID: 10,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if ready, err := store.MarkReadyForSession(ctx, job.UserID, job.AuthKeyID, job.SessionID); err != nil || ready != 1 {
		t.Fatalf("mark ready = %d, %v; want 1 nil", ready, err)
	}
	claimed, err := store.ClaimReady(ctx, 10, time.Second)
	if err != nil {
		t.Fatalf("claim ready: %v", err)
	}
	if len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("claimed = %+v, want one first attempt", claimed)
	}
	if err := store.MarkFailed(ctx, claimed[0].ID, "temporary"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	retry, err := store.ClaimReady(ctx, 10, time.Second)
	if err != nil {
		t.Fatalf("claim retry before backoff: %v", err)
	}
	if len(retry) != 0 {
		t.Fatalf("retry before backoff = %+v, want none", retry)
	}

	store.mu.Lock()
	job = store.jobs[claimed[0].ID]
	job.ReadyAt = time.Now().Add(-time.Second)
	store.jobs[claimed[0].ID] = job
	store.mu.Unlock()

	retry, err = store.ClaimReady(ctx, 10, time.Second)
	if err != nil {
		t.Fatalf("claim retry after backoff: %v", err)
	}
	if len(retry) != 1 || retry[0].Attempts != 2 {
		t.Fatalf("retry after backoff = %+v, want second attempt", retry)
	}
}
