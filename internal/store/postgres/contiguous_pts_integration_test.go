package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestAppendRejectsPtsHole 用真实 PG 验证显式 pts 写入不能制造空洞。
func TestAppendRejectsPtsHole(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	suffix := randomSuffix(t)
	owner, err := NewUserStore(pool).Create(ctx, domain.User{
		AccessHash: 1,
		Phone:      "+1556" + suffix + "01",
		FirstName:  "Contig",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM user_update_events WHERE user_id = $1", owner.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	events := NewUpdateEventStore(pool)
	appendPts := func(pts int) {
		if err := events.Append(ctx, owner.ID, domain.UpdateEvent{
			Type:     domain.UpdateEventNoop,
			Pts:      pts,
			PtsCount: 1,
			Date:     1700000000 + pts,
		}); err != nil {
			t.Fatalf("append pts=%d: %v", pts, err)
		}
	}

	for _, p := range []int{1, 2, 3} {
		appendPts(p)
	}
	err = events.Append(ctx, owner.ID, domain.UpdateEvent{
		Type:     domain.UpdateEventNoop,
		Pts:      5,
		PtsCount: 1,
		Date:     1700000005,
	})
	if err == nil {
		t.Fatal("append pts=5 succeeded, want gap rejection")
	}
	got, err := events.MaxContiguousPts(ctx, owner.ID)
	if err != nil {
		t.Fatalf("MaxContiguousPts: %v", err)
	}
	if got != 3 {
		t.Fatalf("contiguous = %d, want 3 after rejected gap", got)
	}

	appendPts(4)
	appendPts(5)
	got, err = events.MaxContiguousPts(ctx, owner.ID)
	if err != nil {
		t.Fatalf("MaxContiguousPts after fill: %v", err)
	}
	if got != 5 {
		t.Fatalf("contiguous after ordered append = %d, want 5", got)
	}
}

func TestAppendWithDispatchWritesEventAndOutboxAtomically(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	suffix := randomSuffix(t)
	owner, err := NewUserStore(pool).Create(ctx, domain.User{
		AccessHash: 2,
		Phone:      "+1557" + suffix + "01",
		FirstName:  "Dispatch",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM dispatch_outbox WHERE target_user_id = $1", owner.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM user_update_events WHERE user_id = $1", owner.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	event := domain.UpdateEvent{
		UserID:   owner.ID,
		Type:     domain.UpdateEventDialogPinned,
		Pts:      1,
		PtsCount: 1,
		Date:     1700000001,
		Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
		Settings: domain.PeerSettings{
			ShareContact: true,
		},
		Bool: true,
	}
	var excludeAuthKeyID [8]byte
	excludeAuthKeyID[0] = 9
	if _, err := NewUpdateEventStore(pool).AppendAllocatedWithDispatch(ctx, owner.ID, event, excludeAuthKeyID, 77); err != nil {
		t.Fatalf("AppendAllocatedWithDispatch: %v", err)
	}

	got, err := NewUpdateEventStore(pool).ListAfter(ctx, owner.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListAfter: %v", err)
	}
	if len(got) != 1 || got[0].Type != event.Type || got[0].Peer != event.Peer || !got[0].Bool {
		t.Fatalf("events = %+v, want dialog pinned event", got)
	}

	var outbox struct {
		pts              int
		eventType        string
		excludeAuthKeyID int64
		excludeSessionID int64
	}
	if err := pool.QueryRow(ctx, `
		SELECT pts, event_type, exclude_auth_key_id, exclude_session_id
		FROM dispatch_outbox
		WHERE target_user_id = $1
	`, owner.ID).Scan(&outbox.pts, &outbox.eventType, &outbox.excludeAuthKeyID, &outbox.excludeSessionID); err != nil {
		t.Fatalf("query dispatch outbox: %v", err)
	}
	if outbox.pts != 1 || outbox.eventType != string(domain.UpdateEventDialogPinned) || outbox.excludeAuthKeyID != authKeyIDToInt64(excludeAuthKeyID) || outbox.excludeSessionID != 77 {
		t.Fatalf("outbox = %+v, want event dispatch excluding current session", outbox)
	}
}

func TestDispatchOutboxLifecycleKeepsDurableEvents(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	suffix := randomSuffix(t)
	owner, err := NewUserStore(pool).Create(ctx, domain.User{
		AccessHash: 3,
		Phone:      "+1558" + suffix + "01",
		FirstName:  "Outbox",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	events := NewUpdateEventStore(tx)
	outbox := NewDispatchOutboxStore(tx, WithLeaseTimeout(time.Second))
	appendEvent := func(pts int, sessionID int64) {
		t.Helper()
		event := domain.UpdateEvent{
			UserID:   owner.ID,
			Type:     domain.UpdateEventDialogPinned,
			Pts:      pts,
			PtsCount: 1,
			Date:     1700000100 + pts,
			Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID + int64(pts)},
			Bool:     pts%2 == 0,
		}
		if _, err := events.AppendAllocatedWithDispatch(ctx, owner.ID, event, [8]byte{1}, sessionID); err != nil {
			t.Fatalf("AppendAllocatedWithDispatch pts=%d: %v", pts, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE dispatch_outbox
			SET next_attempt_at = now() - interval '10 years',
			    updated_at = now() - interval '10 years'
			WHERE target_user_id = $1
			  AND pts = $2
		`, owner.ID, pts); err != nil {
			t.Fatalf("make outbox ready pts=%d: %v", pts, err)
		}
	}
	outboxRows := func(pts int) int {
		t.Helper()
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT count(*)::int
			FROM dispatch_outbox
			WHERE target_user_id = $1
			  AND pts = $2
		`, owner.ID, pts).Scan(&count); err != nil {
			t.Fatalf("count outbox pts=%d: %v", pts, err)
		}
		return count
	}
	eventRows := func(pts int) int {
		t.Helper()
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT count(*)::int
			FROM user_update_events
			WHERE user_id = $1
			  AND pts = $2
		`, owner.ID, pts).Scan(&count); err != nil {
			t.Fatalf("count event pts=%d: %v", pts, err)
		}
		return count
	}

	appendEvent(1, 101)
	claimed, err := outbox.ClaimPending(ctx, 1)
	if err != nil {
		t.Fatalf("ClaimPending first event: %v", err)
	}
	if len(claimed) != 1 || claimed[0].TargetUserID != owner.ID || claimed[0].Pts != 1 || claimed[0].Attempts != 1 || claimed[0].ExcludeSessionID != 101 {
		t.Fatalf("claimed first = %+v, want owner pts=1 attempts=1", claimed)
	}
	if err := outbox.MarkDelivered(ctx, claimed[0]); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	if got := outboxRows(1); got != 0 {
		t.Fatalf("outbox rows for delivered pts=1 = %d, want 0", got)
	}
	if got := eventRows(1); got != 1 {
		t.Fatalf("durable event rows for delivered pts=1 = %d, want 1", got)
	}

	appendEvent(2, 102)
	if _, err := tx.Exec(ctx, `
		UPDATE dispatch_outbox
		SET status = 'dispatching',
		    attempts = 1,
		    next_attempt_at = now() - interval '10 years',
		    updated_at = now() - interval '10 years'
		WHERE target_user_id = $1
		  AND pts = 2
	`, owner.ID); err != nil {
		t.Fatalf("make stale dispatching: %v", err)
	}
	claimed, err = outbox.ClaimPending(ctx, 1)
	if err != nil {
		t.Fatalf("ClaimPending stale event: %v", err)
	}
	if len(claimed) != 1 || claimed[0].TargetUserID != owner.ID || claimed[0].Pts != 2 || claimed[0].Attempts != 2 {
		t.Fatalf("claimed stale = %+v, want owner pts=2 attempts=2", claimed)
	}
	if err := outbox.MarkFailed(ctx, claimed[0], "temporary"); err != nil {
		t.Fatalf("MarkFailed temporary: %v", err)
	}
	var status string
	var attempts int
	var lastError string
	var retryFuture bool
	if err := tx.QueryRow(ctx, `
		SELECT status, attempts, last_error, next_attempt_at > now()
		FROM dispatch_outbox
		WHERE target_user_id = $1
		  AND id = $2
	`, owner.ID, claimed[0].ID).Scan(&status, &attempts, &lastError, &retryFuture); err != nil {
		t.Fatalf("query temporary failure: %v", err)
	}
	if status != "pending" || attempts != 2 || lastError != "temporary" || !retryFuture {
		t.Fatalf("temporary failure status=%s attempts=%d err=%q retryFuture=%v, want pending attempts=2 future retry", status, attempts, lastError, retryFuture)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE dispatch_outbox
		SET status = 'dispatching',
		    attempts = 5,
		    updated_at = now() - interval '10 years'
		WHERE target_user_id = $1
		  AND id = $2
	`, owner.ID, claimed[0].ID); err != nil {
		t.Fatalf("prepare terminal failure: %v", err)
	}
	claimed[0].Attempts = 5
	if err := outbox.MarkFailed(ctx, claimed[0], "permanent"); err != nil {
		t.Fatalf("MarkFailed permanent: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT status, attempts, last_error
		FROM dispatch_outbox
		WHERE target_user_id = $1
		  AND id = $2
	`, owner.ID, claimed[0].ID).Scan(&status, &attempts, &lastError); err != nil {
		t.Fatalf("query terminal failure: %v", err)
	}
	if status != "failed" || attempts != 5 || lastError != "permanent" {
		t.Fatalf("terminal failure status=%s attempts=%d err=%q, want failed attempts=5", status, attempts, lastError)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE dispatch_outbox
		SET updated_at = now() - interval '10 years'
		WHERE target_user_id = $1
		  AND id = $2
	`, owner.ID, claimed[0].ID); err != nil {
		t.Fatalf("age failed row: %v", err)
	}
	deleted, err := outbox.DeleteFailed(ctx, 24*time.Hour, 1)
	if err != nil {
		t.Fatalf("DeleteFailed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("DeleteFailed deleted=%d, want 1", deleted)
	}
	if got := outboxRows(2); got != 0 {
		t.Fatalf("outbox rows for failed pts=2 = %d, want 0", got)
	}
	if got := eventRows(2); got != 1 {
		t.Fatalf("durable event rows for failed pts=2 = %d, want 1", got)
	}
}
