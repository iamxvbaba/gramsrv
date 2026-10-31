package store

import (
	"context"

	"telesrv/internal/domain"
)

// UpdateEventStore persists incremental events at the user dimension.
type UpdateEventStore interface {
	Append(ctx context.Context, userID int64, event domain.UpdateEvent) error
	AppendAllocated(ctx context.Context, userID int64, event domain.UpdateEvent) (domain.UpdateEvent, error)
	ListAfter(ctx context.Context, userID int64, pts, limit int) ([]domain.UpdateEvent, error)
	// MaxContiguousPts returns the account's committed pts watermark. The PG implementation
	// places pts allocation and event write within the same transaction boundary, so this
	// watermark is the durable log's contiguous watermark.
	MaxContiguousPts(ctx context.Context, userID int64) (int, error)
}

// EventCursor precisely locates one account event, used by the outbox worker to batch-load claimed events.
type EventCursor struct {
	UserID int64
	Pts    int
}
