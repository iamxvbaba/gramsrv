package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// defaultDispatchLease is the default duration after which a 'dispatching' row is judged to have
// an expired lease and may be re-claimed. It corresponds to the outbox backpressure parameter in
// docs/message-module.md; production overrides it via config injection.
const (
	defaultDispatchLease              = 30 * time.Second
	defaultDispatchPoisonCleanupBatch = 256
	maxDispatchPoisonCleanupBatch     = 1000
)

var errInvalidDispatchOutboxExclusionPair = errors.New("dispatch outbox exclusion requires both raw auth key and session id")

// enqueueDispatch is the only production write boundary for dispatch_outbox.
// A zero pair means no originating session is excluded; a non-zero pair identifies
// one exact physical raw-auth/session tuple. A half pair is never meaningful because
// session IDs are not globally unique and must fail the surrounding transaction.
func enqueueDispatch(ctx context.Context, q *sqlcgen.Queries, arg sqlcgen.EnqueueDispatchParams) error {
	hasAuthKey := arg.ExcludeAuthKeyID != 0
	hasSession := arg.ExcludeSessionID != 0
	if hasAuthKey != hasSession {
		return errInvalidDispatchOutboxExclusionPair
	}
	return q.EnqueueDispatch(ctx, arg)
}

// DispatchOutboxStore implements the transactional outbox with PostgreSQL.
type DispatchOutboxStore struct {
	db           sqlcgen.DBTX
	q            *sqlcgen.Queries
	leaseSeconds int32
}

// DispatchOutboxOption adjusts the DispatchOutboxStore claim behavior.
type DispatchOutboxOption func(*DispatchOutboxStore)

// WithLeaseTimeout sets the lease timeout; <=0 keeps the default.
func WithLeaseTimeout(d time.Duration) DispatchOutboxOption {
	return func(s *DispatchOutboxStore) {
		if d > 0 {
			s.leaseSeconds = int32(d / time.Second)
			if s.leaseSeconds < 1 {
				s.leaseSeconds = 1
			}
		}
	}
}

// NewDispatchOutboxStore creates a DispatchOutboxStore backed by a pgx pool (or transaction).
func NewDispatchOutboxStore(db sqlcgen.DBTX, opts ...DispatchOutboxOption) *DispatchOutboxStore {
	s := &DispatchOutboxStore{
		db:           db,
		q:            sqlcgen.New(db),
		leaseSeconds: int32(defaultDispatchLease / time.Second),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func (s *DispatchOutboxStore) ClaimPending(ctx context.Context, limit int) ([]store.DispatchOutboxItem, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.q.ClaimDispatchOutbox(ctx, sqlcgen.ClaimDispatchOutboxParams{
		LeaseSeconds: s.leaseSeconds,
		LimitCount:   int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("claim dispatch outbox: %w", err)
	}
	return dispatchItemsFromClaimRows(rows), nil
}

// ClaimPendingShards claims only user head events within a fixed logical shard set.
// shardCount is the stable hash space and shardIDs the subset currently owned by this worker;
// a change in worker count only changes the runtime shard→worker ownership, not user→shard,
// thereby avoiding the same user being claimed in parallel.
func (s *DispatchOutboxStore) ClaimPendingShards(ctx context.Context, shardCount int, shardIDs []int, limit int) ([]store.DispatchOutboxItem, error) {
	if shardCount <= 0 || len(shardIDs) == 0 {
		return nil, nil
	}
	if shardCount != store.DispatchOutboxLogicalShards {
		return nil, fmt.Errorf("claim dispatch outbox shards: shard count %d, want stable %d", shardCount, store.DispatchOutboxLogicalShards)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	ids := make([]int16, 0, len(shardIDs))
	seen := make(map[int]struct{}, len(shardIDs))
	for _, id := range shardIDs {
		if id < 0 || id >= shardCount {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, int16(id))
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.q.ClaimDispatchOutboxShards(ctx, sqlcgen.ClaimDispatchOutboxShardsParams{
		LeaseSeconds: s.leaseSeconds,
		LimitCount:   int32(limit),
		ShardIds:     ids,
	})
	if err != nil {
		return nil, fmt.Errorf("claim dispatch outbox shards: %w", err)
	}
	out := make([]store.DispatchOutboxItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.DispatchOutboxItem{
			ID:               row.ID,
			TargetUserID:     row.TargetUserID,
			Pts:              int(row.Pts),
			EventType:        domain.UpdateEventType(row.EventType),
			ExcludeAuthKeyID: authKeyIDFromInt64(row.ExcludeAuthKeyID),
			ExcludeSessionID: row.ExcludeSessionID,
			Attempts:         int(row.Attempts),
		})
	}
	return out, nil
}

func dispatchItemsFromClaimRows(rows []sqlcgen.ClaimDispatchOutboxRow) []store.DispatchOutboxItem {
	out := make([]store.DispatchOutboxItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.DispatchOutboxItem{
			ID:               row.ID,
			TargetUserID:     row.TargetUserID,
			Pts:              int(row.Pts),
			EventType:        domain.UpdateEventType(row.EventType),
			ExcludeAuthKeyID: authKeyIDFromInt64(row.ExcludeAuthKeyID),
			ExcludeSessionID: row.ExcludeSessionID,
			Attempts:         int(row.Attempts),
		})
	}
	return out
}

// MarkDeliveredBatch deletes a batch of delivered outbox rows in one go (plan A: delete on
// successful delivery), replacing per-row MarkDelivered.
func (s *DispatchOutboxStore) MarkDeliveredBatch(ctx context.Context, items []store.DispatchOutboxItem) error {
	if len(items) == 0 {
		return nil
	}
	targetUserIDs := make([]int64, len(items))
	ids := make([]int64, len(items))
	expectedAttempts := make([]int32, len(items))
	for i, it := range items {
		targetUserIDs[i] = it.TargetUserID
		ids[i] = it.ID
		expectedAttempts[i] = int32(it.Attempts)
	}
	rows, err := s.withExclusiveLaneFences(ctx, targetUserIDs, func(db sqlcgen.DBTX) (int64, error) {
		return sqlcgen.New(db).MarkDispatchDeliveredBatch(ctx, sqlcgen.MarkDispatchDeliveredBatchParams{
			TargetUserIds:    targetUserIDs,
			Ids:              ids,
			ExpectedAttempts: expectedAttempts,
		})
	})
	if err != nil {
		return fmt.Errorf("mark dispatch delivered batch: %w", err)
	}
	if rows != int64(len(items)) {
		return fmt.Errorf("mark dispatch delivered batch: %w: updated %d of %d", store.ErrDispatchLeaseLost, rows, len(items))
	}
	return nil
}

func (s *DispatchOutboxStore) MarkDelivered(ctx context.Context, item store.DispatchOutboxItem) error {
	rows, err := s.withExclusiveLaneFences(ctx, []int64{item.TargetUserID}, func(db sqlcgen.DBTX) (int64, error) {
		return sqlcgen.New(db).MarkDispatchDelivered(ctx, sqlcgen.MarkDispatchDeliveredParams{
			TargetUserID:     item.TargetUserID,
			ID:               item.ID,
			ExpectedAttempts: int32(item.Attempts),
		})
	})
	if err != nil {
		return fmt.Errorf("mark dispatch delivered: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("mark dispatch delivered: %w", store.ErrDispatchLeaseLost)
	}
	return nil
}

// withExclusiveLaneFences serializes the empty-lane transition with producers'
// shared append fences. The DELETE runs as a later READ COMMITTED statement, so
// it sees every producer that committed before the exclusive fence was granted.
func (s *DispatchOutboxStore) withExclusiveLaneFences(
	ctx context.Context,
	userIDs []int64,
	work func(sqlcgen.DBTX) (int64, error),
) (int64, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return 0, fmt.Errorf("dispatch lane transition requires transaction-capable database")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin dispatch lane transition: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := lockDispatchOutboxLanesExclusive(ctx, tx, userIDs); err != nil {
		return 0, err
	}
	rows, err := work(tx)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit dispatch lane transition: %w", err)
	}
	committed = true
	return rows, nil
}

func lockDispatchOutboxLanesExclusive(ctx context.Context, db sqlcgen.DBTX, userIDs []int64) error {
	unique := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		unique = append(unique, userID)
	}
	if len(unique) == 0 {
		return nil
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	if _, err := db.Exec(ctx, `
SELECT pg_advisory_xact_lock(dispatch_outbox_lane_advisory_key(streams.target_user_id))
FROM unnest($1::bigint[]) AS streams(target_user_id)
ORDER BY streams.target_user_id`, unique); err != nil {
		return fmt.Errorf("lock dispatch outbox lane transition: %w", err)
	}
	return nil
}

func (s *DispatchOutboxStore) MarkFailed(ctx context.Context, item store.DispatchOutboxItem, lastError string) error {
	rows, err := s.q.MarkDispatchFailed(ctx, sqlcgen.MarkDispatchFailedParams{
		TargetUserID:     item.TargetUserID,
		ID:               item.ID,
		LastError:        lastError,
		ExpectedAttempts: int32(item.Attempts),
	})
	if err != nil {
		return fmt.Errorf("mark dispatch failed: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("mark dispatch failed: %w", store.ErrDispatchLeaseLost)
	}
	return nil
}

func (s *DispatchOutboxStore) DeleteFailed(ctx context.Context, olderThan time.Duration, limit int) (int, error) {
	if olderThan <= 0 {
		olderThan = time.Minute
	}
	if limit <= 0 {
		limit = defaultDispatchPoisonCleanupBatch
	}
	if limit > maxDispatchPoisonCleanupBatch {
		limit = maxDispatchPoisonCleanupBatch
	}
	olderThanSeconds := int32(olderThan / time.Second)
	rows, err := s.db.Query(ctx, `
SELECT h.target_user_id
FROM dispatch_outbox_user_heads h
WHERE h.status = 'failed'
  AND h.updated_at < now() - make_interval(secs => $1::int)
ORDER BY h.updated_at ASC, h.target_user_id ASC, h.head_id ASC
LIMIT $2`, olderThanSeconds, int32(limit))
	if err != nil {
		return 0, fmt.Errorf("list failed dispatch outbox lanes: %w", err)
	}
	userIDs := make([]int64, 0, limit)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan failed dispatch outbox lane: %w", err)
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate failed dispatch outbox lanes: %w", err)
	}
	rows.Close()
	if len(userIDs) == 0 {
		return 0, nil
	}

	deleted, err := s.withExclusiveLaneFences(ctx, userIDs, func(db sqlcgen.DBTX) (int64, error) {
		count, deleteErr := sqlcgen.New(db).DeleteFailedDispatchOutbox(ctx, sqlcgen.DeleteFailedDispatchOutboxParams{
			OlderThanSeconds: olderThanSeconds,
			LimitCount:       int32(limit),
			TargetUserIds:    userIDs,
		})
		return int64(count), deleteErr
	})
	if err != nil {
		return 0, fmt.Errorf("delete failed dispatch outbox: %w", err)
	}
	return int(deleted), nil
}
