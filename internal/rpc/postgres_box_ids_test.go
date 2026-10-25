package rpc

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/store/postgres"
)

// rpcTestPostgresBoxIDs stands in for the Redis box id allocator in rpc
// integration tests. Production MessageStore refuses to send without an
// allocator, so tests that drive real private sends through PostgreSQL need one
// seeded from the durable counters, like the postgres package's own test kit.
type rpcTestPostgresBoxIDs struct {
	mu     sync.Mutex
	source *postgres.MessageBoxCounterSource
	values map[int64]int
}

func newRPCTestPostgresMessageStore(pool *pgxpool.Pool) *postgres.MessageStore {
	return postgres.NewMessageStore(pool, postgres.WithMessageAllocators(&rpcTestPostgresBoxIDs{
		source: postgres.NewMessageBoxCounterSource(pool),
		values: make(map[int64]int),
	}))
}

func (a *rpcTestPostgresBoxIDs) NextBoxID(ctx context.Context, userID int64) (int, error) {
	values, err := a.NextBoxIDs(ctx, []int64{userID})
	if err != nil {
		return 0, err
	}
	return values[userID], nil
}

func (a *rpcTestPostgresBoxIDs) NextBoxIDs(ctx context.Context, userIDs []int64) (map[int64]int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	current, err := a.source.CurrentBatch(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int, len(userIDs))
	for _, userID := range userIDs {
		if current[userID] > a.values[userID] {
			a.values[userID] = current[userID]
		}
		a.values[userID]++
		out[userID] = a.values[userID]
	}
	return out, nil
}

func (a *rpcTestPostgresBoxIDs) CurrentBoxID(ctx context.Context, userID int64) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	current, err := a.source.Current(ctx, userID)
	if err != nil {
		return 0, err
	}
	if a.values[userID] > current {
		return a.values[userID], nil
	}
	return current, nil
}
