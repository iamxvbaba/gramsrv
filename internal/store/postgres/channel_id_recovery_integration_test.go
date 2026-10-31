package postgres

import (
	"context"
	"sync/atomic"
	"testing"

	"telesrv/internal/domain"
)

// staleChannelIDAllocator simulates a lagging channel id counter: it steps by
// 1 from start, inevitably colliding with an existing channel primary key.
type staleChannelIDAllocator struct {
	next    atomic.Int64
	atLeast atomic.Int64 // records the floor when NextChannelIDAtLeast is called
}

func (a *staleChannelIDAllocator) NextChannelID(_ context.Context) (int64, error) {
	return a.next.Add(1), nil
}

func (a *staleChannelIDAllocator) CurrentChannelID(_ context.Context) (int64, error) {
	return a.next.Load(), nil
}

func (a *staleChannelIDAllocator) NextChannelIDAtLeast(_ context.Context, floor int64) (int64, error) {
	a.atLeast.Store(floor)
	for {
		cur := a.next.Load()
		if cur < floor {
			if !a.next.CompareAndSwap(cur, floor) {
				continue
			}
		}
		return a.next.Add(1), nil
	}
}

// TestChannelStoreCreateChannelRecoversFromStaleIDCounter verifies that when the counter lags
// (Redis snapshot rollback / the test fallback allocator bypassing Redis and writing directly to the same DB),
// CreateChannel self-heals through the pre-check to get a free id, instead of a primary-key 500.
func TestChannelStoreCreateChannelRecoversFromStaleIDCounter(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	creator, err := users.Create(ctx, domain.User{AccessHash: 91, Phone: "+1673" + suffix + "01", FirstName: "StaleIDCreator"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", creator.ID)
	})

	// First create a real channel with the default PG fallback allocator (occupying the max id).
	seedStore := NewChannelStore(pool)
	seed, err := seedStore.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "StaleSeed " + suffix,
		Megagroup:     true,
		Date:          1700000700,
	})
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	// The lagging counter starts far before the seed id (simulating a rollback); the first allocation must collide.
	stale := &staleChannelIDAllocator{}
	stale.next.Store(seed.Channel.ID - 3)
	staleStore := NewChannelStore(pool, WithChannelAllocators(stale, nil))
	// Passing nil msgIDs gets overridden by the fallback; channel pts is maintained directly by the PG transaction.
	created, err := staleStore.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "StaleRecovered " + suffix,
		Megagroup:     true,
		Date:          1700000701,
	})
	if err != nil {
		t.Fatalf("create channel with stale counter: %v", err)
	}
	if created.Channel.ID <= seed.Channel.ID {
		t.Fatalf("recovered channel id = %d, want > seed id %d", created.Channel.ID, seed.Channel.ID)
	}
	if stale.atLeast.Load() < seed.Channel.ID {
		t.Fatalf("NextChannelIDAtLeast floor = %d, want >= seed id %d (should reconcile to the table's max id)", stale.atLeast.Load(), seed.Channel.ID)
	}
}
