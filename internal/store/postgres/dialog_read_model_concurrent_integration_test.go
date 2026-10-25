package postgres

import (
	"context"
	"testing"
	"time"
)

// TestDialogReadModelBumpsTwoPeersOfSameOwnerConcurrently is the regression
// for migration 0206. The advisory lock inside telesrv_bump_dialog_light must
// be owner-scoped, not (owner, peer): the dialog_owner row is a single row
// shared by every peer of the owner, so two transactions that each bump two
// peers of the same owner in opposite order used to deadlock (Postgres 40P01)
// -- txn A held advisory(owner, A)+dialog_owner while txn B held
// advisory(owner, B) and waited for dialog_owner, then A waited for
// advisory(owner, B).
func TestDialogReadModelBumpsTwoPeersOfSameOwnerConcurrently(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	suffix := time.Now().UnixNano() % 1_000_000_000
	ownerID := int64(6_100_000_000) + suffix
	peerA := ownerID + 1001
	peerB := ownerID + 1002

	// Each bump must execute on its transaction, not on the pool. Hold txn1's
	// first owner lock while txn2 attempts the other peer, then verify txn2 is
	// actually waiting before txn1 bumps its second peer.
	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	if _, err := tx1.Exec(ctx, `SELECT public.telesrv_bump_dialog_light($1, 'user', $2)`, ownerID, peerA); err != nil {
		t.Fatalf("txn1 bump A: %v", err)
	}
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback(ctx) }()
	var tx2PID int
	if err := tx2.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&tx2PID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := tx2.Exec(ctx, `SELECT public.telesrv_bump_dialog_light($1, 'user', $2)`, ownerID, peerB)
		if err == nil {
			_, err = tx2.Exec(ctx, `SELECT public.telesrv_bump_dialog_light($1, 'user', $2)`, ownerID, peerA)
		}
		done <- err
	}()
	blocked := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND NOT granted)`, tx2PID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("txn2 did not wait on the owner advisory lock")
	}
	if _, err := tx1.Exec(ctx, `SELECT public.telesrv_bump_dialog_light($1, 'user', $2)`, ownerID, peerB); err != nil {
		t.Fatalf("txn1 bump B while txn2 waits: %v", err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("txn1 commit: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("txn2 bumps: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("txn2 did not complete after txn1 committed")
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("txn2 commit: %v", err)
	}
}
