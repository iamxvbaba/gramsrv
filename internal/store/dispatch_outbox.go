package store

import (
	"context"
	"errors"
	"time"

	"telesrv/internal/domain"
)

// ErrDispatchLeaseLost means a completion belongs to an older claim attempt.
// Callers must not overwrite/delete the row now owned by a newer worker.
var ErrDispatchLeaseLost = errors.New("dispatch outbox lease lost")

// DispatchOutboxLogicalShards 是稳定的 user→lane 哈希空间。运行时 worker 数
// 只能改变 shard 的归属，不能改变这个值；PG 表达式索引也固定使用 256。
const DispatchOutboxLogicalShards = 256

// DispatchOutboxItem 是待投递给在线 session 的 update 任务。
type DispatchOutboxItem struct {
	ID               int64
	TargetUserID     int64
	Pts              int
	EventType        domain.UpdateEventType
	ExcludeAuthKeyID [8]byte
	ExcludeSessionID int64
	Attempts         int
}

// DispatchOutboxStore 持久化 transactional outbox。
type DispatchOutboxStore interface {
	ClaimPending(ctx context.Context, limit int) ([]DispatchOutboxItem, error)
	MarkDelivered(ctx context.Context, item DispatchOutboxItem) error
	MarkFailed(ctx context.Context, item DispatchOutboxItem, lastError string) error
	DeleteFailed(ctx context.Context, olderThan time.Duration, limit int) (int, error)
}
