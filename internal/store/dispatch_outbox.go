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

// DispatchOutboxLogicalShards is the stable user->lane hash space. The runtime worker
// count may only change shard ownership, not this value; the PG expression index is also
// fixed at 256.
const DispatchOutboxLogicalShards = 256

// DispatchOutboxItem is an update task waiting to be delivered to an online session.
type DispatchOutboxItem struct {
	ID               int64
	TargetUserID     int64
	Pts              int
	EventType        domain.UpdateEventType
	ExcludeAuthKeyID [8]byte
	ExcludeSessionID int64
	Attempts         int
}

// DispatchOutboxStore persists the transactional outbox.
type DispatchOutboxStore interface {
	ClaimPending(ctx context.Context, limit int) ([]DispatchOutboxItem, error)
	MarkDelivered(ctx context.Context, item DispatchOutboxItem) error
	MarkFailed(ctx context.Context, item DispatchOutboxItem, lastError string) error
	DeleteFailed(ctx context.Context, olderThan time.Duration, limit int) (int, error)
}
