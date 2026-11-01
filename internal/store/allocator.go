package store

import "context"

// BoxIDAllocator allocates user-visible message box ids. box_id may have gaps but must not go backwards.
type BoxIDAllocator interface {
	NextBoxID(ctx context.Context, userID int64) (int, error)
	// NextBoxIDs allocates one monotonic id for every distinct user as one
	// backend batch. The steady-state path must use one network round trip;
	// implementations must reject invalid users before allocating anything
	// and must not fall back to per-user calls.
	NextBoxIDs(ctx context.Context, userIDs []int64) (map[int64]int, error)
	CurrentBoxID(ctx context.Context, userID int64) (int, error)
}

// CounterSource restores the current value from the PostgreSQL durable log when a Redis counter cold-starts.
type CounterSource interface {
	Current(ctx context.Context, userID int64) (int, error)
	// CurrentBatch returns the durable floor for every requested counter in one
	// backend operation. Redis cold-start recovery must not fall back to a
	// per-counter loop because private-message allocation commonly misses two
	// owner counters at once after a cache flush.
	CurrentBatch(ctx context.Context, userIDs []int64) (map[int64]int, error)
}
