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
	next atomic.Int64
}

func (a *staleChannelIDAllocator) NextChannelID(_ context.Context) (int64, error) {
	return a.next.Add(1), nil
}

func (a *staleChannelIDAllocator) CurrentChannelID(_ context.Context) (int64, error) {
	return a.next.Load(), nil
}

// TestChannelStoreCreateChannelRejectsStaleIDCounter locks the single-authority
// contract: a colliding allocator result fails at the unique boundary and is
// never reconciled from PostgreSQL MAX(id) or retried through another path.
func TestChannelStoreCreateChannelRejectsStaleIDCounter(t *testing.T) {
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

	var maxID int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM channels`).Scan(&maxID); err != nil {
		t.Fatalf("load current max channel id: %v", err)
	}
	seedIDs := &staleChannelIDAllocator{}
	seedIDs.next.Store(maxID)
	messageIDs := testAllocatorsFor(pool).channelMessageIDs
	seedStore := newTestChannelStore(pool, WithChannelAllocators(seedIDs, messageIDs))
	seed, err := seedStore.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "StaleSeed " + suffix,
		Megagroup:     true,
		Date:          1700000700,
	})
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	// The stale authority returns the already committed seed id.
	stale := &staleChannelIDAllocator{}
	stale.next.Store(seed.Channel.ID - 1)
	staleStore := newTestChannelStore(pool, WithChannelAllocators(stale, messageIDs))
	_, err = staleStore.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "StaleRecovered " + suffix,
		Megagroup:     true,
		Date:          1700000701,
	})
	if err == nil {
		t.Fatal("create channel with colliding authority unexpectedly succeeded")
	}
	if stale.next.Load() != seed.Channel.ID {
		t.Fatalf("allocator advanced after collision: got %d want %d", stale.next.Load(), seed.Channel.ID)
	}
}
