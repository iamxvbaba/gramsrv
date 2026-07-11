package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	storepkg "telesrv/internal/store"
)

func TestDispatchOutboxUserHeadBlocksHigherPts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner := createTestUser(t, ctx, NewUserStore(pool), "+1884"+suffix+"01", "OutboxHead", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// ClaimPending is intentionally global. Isolate this transaction from durable tasks left by
	// earlier integration tests; the rollback restores those rows after this case completes.
	if _, err := tx.Exec(ctx, `DELETE FROM dispatch_outbox`); err != nil {
		t.Fatalf("isolate dispatch outbox: %v", err)
	}
	events := NewUpdateEventStore(tx)
	outbox := NewDispatchOutboxStore(tx, WithLeaseTimeout(time.Hour))
	appendEvent := func() int {
		t.Helper()
		event, err := events.AppendAllocatedWithDispatch(ctx, owner.ID, domain.UpdateEvent{
			Type:     domain.UpdateEventDialogPinned,
			PtsCount: 1,
			Date:     1700002000,
			Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
			Bool:     true,
		}, [8]byte{}, 0)
		if err != nil {
			t.Fatalf("append event: %v", err)
		}
		return event.Pts
	}
	shard := int(owner.ID % int64(storepkg.DispatchOutboxLogicalShards))

	pts1, pts2 := appendEvent(), appendEvent()
	assertDispatchHead := func(wantPts int) {
		t.Helper()
		var gotPts int
		err := tx.QueryRow(ctx, `
SELECT head_pts
FROM dispatch_outbox_user_heads
WHERE target_user_id = $1
`, owner.ID).Scan(&gotPts)
		if err != nil {
			t.Fatalf("load durable dispatch head: %v", err)
		}
		if gotPts != wantPts {
			t.Fatalf("durable dispatch head pts = %d, want %d", gotPts, wantPts)
		}
	}
	assertDispatchHead(pts1)
	wrongShard := (shard + 1) % storepkg.DispatchOutboxLogicalShards
	if wrong, err := outbox.ClaimPendingShards(ctx, storepkg.DispatchOutboxLogicalShards, []int{wrongShard}, 100); err != nil || len(wrong) != 0 {
		t.Fatalf("wrong-shard claim = %+v err=%v, want empty", wrong, err)
	}
	claimed, err := outbox.ClaimPending(ctx, 100)
	if err != nil {
		t.Fatalf("claim head: %v", err)
	}
	if len(claimed) != 1 || claimed[0].TargetUserID != owner.ID || claimed[0].Pts != pts1 {
		t.Fatalf("first claim = %+v, want only pts %d", claimed, pts1)
	}
	if blocked, err := outbox.ClaimPending(ctx, 100); err != nil || len(blocked) != 0 {
		t.Fatalf("claim behind live dispatching head = %+v err=%v, want empty (pts %d blocked)", blocked, err, pts2)
	}
	if blocked, err := outbox.ClaimPendingShards(ctx, storepkg.DispatchOutboxLogicalShards, []int{shard}, 100); err != nil || len(blocked) != 0 {
		t.Fatalf("shard claim behind live dispatching head = %+v err=%v, want empty", blocked, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE dispatch_outbox SET updated_at = now() - interval '2 hours' WHERE target_user_id = $1 AND id = $2`, owner.ID, claimed[0].ID); err != nil {
		t.Fatalf("age dispatch lease: %v", err)
	}
	reclaimed, err := outbox.ClaimPending(ctx, 100)
	if err != nil {
		t.Fatalf("reclaim stale head: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].Pts != pts1 || reclaimed[0].Attempts != 2 {
		t.Fatalf("stale reclaim = %+v, want pts %d attempts 2", reclaimed, pts1)
	}
	if err := outbox.MarkDelivered(ctx, claimed[0]); !errors.Is(err, storepkg.ErrDispatchLeaseLost) {
		t.Fatalf("old lease delivered err = %v, want ErrDispatchLeaseLost", err)
	}
	if err := outbox.MarkFailed(ctx, claimed[0], "stale worker"); !errors.Is(err, storepkg.ErrDispatchLeaseLost) {
		t.Fatalf("old lease failed err = %v, want ErrDispatchLeaseLost", err)
	}
	var fencedStatus string
	var fencedAttempts int
	if err := tx.QueryRow(ctx, `SELECT status, attempts FROM dispatch_outbox WHERE target_user_id = $1 AND id = $2`, owner.ID, reclaimed[0].ID).Scan(&fencedStatus, &fencedAttempts); err != nil {
		t.Fatalf("load fenced head: %v", err)
	}
	if fencedStatus != "dispatching" || fencedAttempts != 2 {
		t.Fatalf("fenced head = status %s attempts %d, want dispatching/2", fencedStatus, fencedAttempts)
	}
	if err := outbox.MarkDelivered(ctx, reclaimed[0]); err != nil {
		t.Fatalf("deliver head: %v", err)
	}
	assertDispatchHead(pts2)
	next, err := outbox.ClaimPendingShards(ctx, storepkg.DispatchOutboxLogicalShards, []int{shard}, 100)
	if err != nil {
		t.Fatalf("claim next after head delivered: %v", err)
	}
	if len(next) != 1 || next[0].Pts != pts2 {
		t.Fatalf("next claim = %+v, want pts %d", next, pts2)
	}
	if err := outbox.MarkDelivered(ctx, next[0]); err != nil {
		t.Fatalf("deliver second: %v", err)
	}
	var remainingHeads int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM dispatch_outbox_user_heads WHERE target_user_id = $1`, owner.ID).Scan(&remainingHeads); err != nil {
		t.Fatalf("count durable dispatch heads: %v", err)
	}
	if remainingHeads != 0 {
		t.Fatalf("durable dispatch heads after lane drain = %d, want 0", remainingHeads)
	}

	pts3, pts4 := appendEvent(), appendEvent()
	head, err := outbox.ClaimPendingShards(ctx, storepkg.DispatchOutboxLogicalShards, []int{shard}, 100)
	if err != nil {
		t.Fatalf("claim terminal-failure head: %v", err)
	}
	if len(head) != 1 || head[0].Pts != pts3 {
		t.Fatalf("terminal head = %+v, want pts %d", head, pts3)
	}
	if _, err := tx.Exec(ctx, `UPDATE dispatch_outbox SET status = 'dispatching', attempts = 5 WHERE target_user_id = $1 AND id = $2`, owner.ID, head[0].ID); err != nil {
		t.Fatalf("prepare terminal failure: %v", err)
	}
	head[0].Attempts = 5
	if err := outbox.MarkFailed(ctx, head[0], "permanent"); err != nil {
		t.Fatalf("mark terminal failed: %v", err)
	}
	if got, err := outbox.ClaimPending(ctx, 100); err != nil || len(got) != 0 {
		t.Fatalf("global claim behind failed head = %+v err=%v, want empty (pts %d blocked)", got, err, pts4)
	}
	if got, err := outbox.ClaimPendingShards(ctx, storepkg.DispatchOutboxLogicalShards, []int{shard}, 100); err != nil || len(got) != 0 {
		t.Fatalf("shard claim behind failed head = %+v err=%v, want empty", got, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE dispatch_outbox SET updated_at = now() - interval '2 minutes' WHERE target_user_id = $1 AND id = $2`, owner.ID, head[0].ID); err != nil {
		t.Fatalf("age poison head: %v", err)
	}
	if deleted, err := outbox.DeleteFailed(ctx, time.Minute, 1); err != nil || deleted != 1 {
		t.Fatalf("delete quarantined failed head = %d err=%v, want 1", deleted, err)
	}
	var durablePoisonEvent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_update_events WHERE user_id = $1 AND pts = $2`, owner.ID, pts3).Scan(&durablePoisonEvent); err != nil || durablePoisonEvent != 1 {
		t.Fatalf("durable poison event count = %d err=%v, want 1 for difference recovery", durablePoisonEvent, err)
	}
	assertDispatchHead(pts4)
	unblocked, err := outbox.ClaimPendingShards(ctx, storepkg.DispatchOutboxLogicalShards, []int{shard}, 100)
	if err != nil {
		t.Fatalf("claim after failed cleanup: %v", err)
	}
	if len(unblocked) != 1 || unblocked[0].Pts != pts4 {
		t.Fatalf("claim after failed cleanup = %+v, want pts %d", unblocked, pts4)
	}
	if err := outbox.MarkDelivered(ctx, unblocked[0]); err != nil {
		t.Fatalf("deliver unblocked: %v", err)
	}

	pts5 := appendEvent()
	tag, err := tx.Exec(ctx, `
INSERT INTO dispatch_outbox (target_user_id, pts, event_type)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING
`, owner.ID, pts5, string(domain.UpdateEventDialogPinned))
	if err != nil {
		t.Fatalf("duplicate enqueue: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatalf("duplicate enqueue rows = %d, want 0 from (user,pts) unique key", tag.RowsAffected())
	}
	var taskCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM dispatch_outbox WHERE target_user_id = $1 AND pts = $2`, owner.ID, pts5).Scan(&taskCount); err != nil || taskCount != 1 {
		t.Fatalf("duplicate task count = %d err=%v, want 1", taskCount, err)
	}
	if _, err := outbox.ClaimPendingShards(ctx, storepkg.DispatchOutboxLogicalShards-1, []int{shard}, 1); err == nil {
		t.Fatal("unstable shard count accepted")
	}
}

func TestDispatchOutboxShardClaimersAreMutuallyExclusive(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	// This case claims through the real pool from two concurrent transactions. Clear stale tasks
	// left by unrelated cases so the assertion measures this one user lane, not suite order.
	if _, err := pool.Exec(ctx, `DELETE FROM dispatch_outbox`); err != nil {
		t.Fatalf("isolate dispatch outbox: %v", err)
	}
	suffix := randomSuffix(t)
	owner := createTestUser(t, ctx, NewUserStore(pool), "+1885"+suffix+"01", "OutboxLane", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM dispatch_outbox WHERE target_user_id = $1", owner.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})
	if _, err := NewUpdateEventStore(pool).AppendAllocatedWithDispatch(ctx, owner.ID, domain.UpdateEvent{
		Type:     domain.UpdateEventDialogPinned,
		PtsCount: 1,
		Date:     1700002100,
		Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		Bool:     true,
	}, [8]byte{}, 0); err != nil {
		t.Fatalf("append event: %v", err)
	}

	outbox := NewDispatchOutboxStore(pool, WithLeaseTimeout(time.Hour))
	shard := int(owner.ID % int64(storepkg.DispatchOutboxLogicalShards))
	start := make(chan struct{})
	results := make(chan []storepkg.DispatchOutboxItem, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			items, err := outbox.ClaimPendingShards(ctx, storepkg.DispatchOutboxLogicalShards, []int{shard}, 1)
			if err != nil {
				errs <- err
				return
			}
			results <- items
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent shard claim: %v", err)
	}
	claimed := 0
	for items := range results {
		claimed += len(items)
	}
	if claimed != 1 {
		t.Fatalf("concurrent claimed rows = %d, want exactly one user head", claimed)
	}
}

func TestDispatchOutboxLeaseExpiryAndBatchCompletionShareLockOrderPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM dispatch_outbox`); err != nil {
		t.Fatalf("isolate dispatch outbox: %v", err)
	}
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	first := createTestUser(t, ctx, users, "+1886"+suffix+"01", "OutboxLockA", "")
	second := createTestUser(t, ctx, users, "+1886"+suffix+"02", "OutboxLockB", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])`, []int64{first.ID, second.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1::bigint[])`, []int64{first.ID, second.ID})
	})
	events := NewUpdateEventStore(pool)
	outbox := NewDispatchOutboxStore(pool, WithLeaseTimeout(time.Second))

	for round := 0; round < 20; round++ {
		for _, userID := range []int64{first.ID, second.ID} {
			if _, err := events.AppendAllocatedWithDispatch(ctx, userID, domain.UpdateEvent{
				Type: domain.UpdateEventDialogPinned, PtsCount: 1, Date: 1_700_030_000 + round,
				Peer: domain.Peer{Type: domain.PeerTypeUser, ID: userID}, Bool: true,
			}, [8]byte{}, 0); err != nil {
				t.Fatalf("round %d append user %d: %v", round, userID, err)
			}
		}
		claimed, err := outbox.ClaimPending(ctx, 2)
		if err != nil || len(claimed) != 2 {
			t.Fatalf("round %d initial claim = %+v err=%v, want 2", round, claimed, err)
		}
		if _, err := pool.Exec(ctx, `
UPDATE dispatch_outbox
SET updated_at = now() - interval '2 seconds'
WHERE (target_user_id, id) IN (($1, $2), ($3, $4))
`, claimed[0].TargetUserID, claimed[0].ID, claimed[1].TargetUserID, claimed[1].ID); err != nil {
			t.Fatalf("round %d age leases: %v", round, err)
		}
		reversed := []storepkg.DispatchOutboxItem{claimed[1], claimed[0]}
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, claimErr := outbox.ClaimPending(ctx, 2)
			errs <- claimErr
		}()
		go func() {
			defer wg.Done()
			<-start
			markErr := outbox.MarkDeliveredBatch(ctx, reversed)
			if errors.Is(markErr, storepkg.ErrDispatchLeaseLost) {
				markErr = nil
			}
			errs <- markErr
		}()
		close(start)
		wg.Wait()
		close(errs)
		for raceErr := range errs {
			if raceErr != nil {
				t.Fatalf("round %d lease/completion race: %v", round, raceErr)
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])`, []int64{first.ID, second.ID}); err != nil {
			t.Fatalf("round %d drain raced rows: %v", round, err)
		}
	}
}

func TestDispatchOutboxDurableHeadRejectsStaleRowReference(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner := createTestUser(t, ctx, NewUserStore(pool), "+1886"+suffix+"01", "OutboxHeadFK", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM dispatch_outbox WHERE target_user_id = $1", owner.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})
	if _, err := NewUpdateEventStore(pool).AppendAllocatedWithDispatch(ctx, owner.ID, domain.UpdateEvent{
		Type: domain.UpdateEventDialogPinned, PtsCount: 1, Date: 1700002200,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, Bool: true,
	}, [8]byte{}, 0); err != nil {
		t.Fatalf("append event: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE dispatch_outbox_user_heads SET head_id = head_id + 1000000 WHERE target_user_id = $1`, owner.ID); err != nil {
		t.Fatalf("stage stale head: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS dispatch_outbox_user_heads_outbox_fkey IMMEDIATE`); err == nil {
		t.Fatal("stale durable head reference unexpectedly satisfied deferred FK")
	}
}
