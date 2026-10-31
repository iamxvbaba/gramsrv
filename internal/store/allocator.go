package store

import "context"

// BoxIDAllocator allocates user-visible message box ids. box_id may have gaps but must not go backwards.
type BoxIDAllocator interface {
	NextBoxID(ctx context.Context, userID int64) (int, error)
	// NextBoxIDs allocates one monotonic id for every distinct user in one
	// backend batch. Implementations reject invalid users before allocation and
	// must not turn this operation into a per-user network loop.
	NextBoxIDs(ctx context.Context, userIDs []int64) (map[int64]int, error)
	CurrentBoxID(ctx context.Context, userID int64) (int, error)
}

// DistributedBoxIDAllocator marks an allocator whose per-owner reservations
// remain atomic across processes and are safe before a PostgreSQL transaction.
// The private-send microbatch path requires this capability; local and test
// allocators stay on the single-command transaction path.
type DistributedBoxIDAllocator interface {
	BoxIDAllocator
	DistributedBoxIDAllocation()
}

// CounterSource restores the current value from the PostgreSQL durable log when a Redis counter cold-starts.
type CounterSource interface {
	Current(ctx context.Context, userID int64) (int, error)
	CurrentBatch(ctx context.Context, userIDs []int64) (map[int64]int, error)
}
