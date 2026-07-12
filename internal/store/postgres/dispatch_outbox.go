package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// defaultDispatchLease 是 'dispatching' 行被判定租约过期、可被重新 claim 的默认时长。
// 与 docs/message-module.md 的 outbox 背压参数对应；生产由 config 注入覆盖。
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

// DispatchOutboxStore 用 PostgreSQL 实现 transactional outbox。
type DispatchOutboxStore struct {
	q            *sqlcgen.Queries
	leaseSeconds int32
}

// DispatchOutboxOption 调整 DispatchOutboxStore 的 claim 行为。
type DispatchOutboxOption func(*DispatchOutboxStore)

// WithLeaseTimeout 设置租约超时；<=0 时保持默认。
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

// NewDispatchOutboxStore 基于 pgx 连接池（或事务）创建 DispatchOutboxStore。
func NewDispatchOutboxStore(db sqlcgen.DBTX, opts ...DispatchOutboxOption) *DispatchOutboxStore {
	s := &DispatchOutboxStore{
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

// ClaimPendingShards 只领取固定 logical shard 集合中的用户 head 事件。
// shardCount 是稳定哈希空间，shardIDs 是当前 worker 独占的子集；worker 数变化只改变
// shard→worker 的运行时归属，不改变 user→shard，从而避免同一用户被并行领取。
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

// MarkDeliveredBatch 一次性删除一批已投递的 outbox 行（方案 A：投递成功即删），取代逐条 MarkDelivered。
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
	rows, err := s.q.MarkDispatchDeliveredBatch(ctx, sqlcgen.MarkDispatchDeliveredBatchParams{
		TargetUserIds:    targetUserIDs,
		Ids:              ids,
		ExpectedAttempts: expectedAttempts,
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
	rows, err := s.q.MarkDispatchDelivered(ctx, sqlcgen.MarkDispatchDeliveredParams{
		TargetUserID:     item.TargetUserID,
		ID:               item.ID,
		ExpectedAttempts: int32(item.Attempts),
	})
	if err != nil {
		return fmt.Errorf("mark dispatch delivered: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("mark dispatch delivered: %w", store.ErrDispatchLeaseLost)
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
	deleted, err := s.q.DeleteFailedDispatchOutbox(ctx, sqlcgen.DeleteFailedDispatchOutboxParams{
		OlderThanSeconds: int32(olderThan / time.Second),
		LimitCount:       int32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("delete failed dispatch outbox: %w", err)
	}
	return int(deleted), nil
}
