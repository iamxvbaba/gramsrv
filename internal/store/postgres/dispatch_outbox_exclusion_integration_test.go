package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
)

func TestDispatchOutboxExclusionPairInvariantPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner := createTestUser(t, ctx, NewUserStore(pool), "+1887"+suffix+"01", "OutboxPair", "")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID) })

	event := domain.UpdateEvent{
		Type:     domain.UpdateEventDialogPinned,
		PtsCount: 1,
		Date:     1700002300,
		Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		Bool:     true,
	}
	for _, test := range []struct {
		name      string
		authKeyID [8]byte
		sessionID int64
	}{
		{name: "auth key only", authKeyID: [8]byte{1}},
		{name: "session only", sessionID: 77},
	} {
		t.Run("write boundary "+test.name, func(t *testing.T) {
			_, err := NewUpdateEventStore(pool).AppendAllocatedWithDispatch(ctx, owner.ID, event, test.authKeyID, test.sessionID)
			if !errors.Is(err, errInvalidDispatchOutboxExclusionPair) {
				t.Fatalf("AppendAllocatedWithDispatch error = %v, want %v", err, errInvalidDispatchOutboxExclusionPair)
			}
		})
	}

	var eventCount int
	if err := pool.QueryRow(ctx, "SELECT count(*)::int FROM user_update_events WHERE user_id = $1", owner.ID).Scan(&eventCount); err != nil {
		t.Fatalf("count events after rejected writes: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("events after rejected writes = %d, want 0 (transaction rollback)", eventCount)
	}

	stored, err := NewUpdateEventStore(pool).AppendAllocated(ctx, owner.ID, event)
	if err != nil {
		t.Fatalf("append durable event for constraint test: %v", err)
	}
	for _, test := range []struct {
		name      string
		authKeyID int64
		sessionID int64
	}{
		{name: "auth key only", authKeyID: 1},
		{name: "session only", sessionID: 77},
	} {
		t.Run("database constraint "+test.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
INSERT INTO dispatch_outbox (
  target_user_id, pts, event_type, exclude_auth_key_id, exclude_session_id
) VALUES ($1, $2, $3, $4, $5)`, owner.ID, stored.Pts, string(stored.Type), test.authKeyID, test.sessionID)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "dispatch_outbox_exclusion_pair_check" {
				t.Fatalf("direct insert error = %v, want check violation from dispatch_outbox_exclusion_pair_check", err)
			}
		})
	}

	var outboxCount int
	if err := pool.QueryRow(ctx, "SELECT count(*)::int FROM dispatch_outbox WHERE target_user_id = $1", owner.ID).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox after rejected inserts: %v", err)
	}
	if outboxCount != 0 {
		t.Fatalf("outbox rows after rejected inserts = %d, want 0", outboxCount)
	}
}
