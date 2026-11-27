package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

// newAllocatorTestChannel creates an owner and a channel so the allocator can be
// exercised against real channel_messages rows (the counter table itself has no
// foreign keys, but the safety-net path reads the message table).
func newAllocatorTestChannel(t *testing.T, pool *pgxpool.Pool, ctx context.Context) int64 {
	t.Helper()
	suffix := randomSuffix(t)
	owner, err := NewUserStore(pool).Create(ctx, domain.User{
		AccessHash: 1811,
		Phone:      "+1788" + suffix + "01",
		FirstName:  "ChannelMsgIDAllocator",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	created, err := NewChannelStore(pool).CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Channel msg id allocator " + suffix,
		Megagroup:     true,
		Date:          1700200000,
	})
	if err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, owner.ID)
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM channel_message_id_counters WHERE channel_id = $1`, channelID)
		_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE id = $1`, channelID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, owner.ID)
	})
	return channelID
}

// insertRawChannelMessage writes a channel_messages row directly, the way a
// restore, manual backfill or an older tool does: bypassing the allocator.
func insertRawChannelMessage(t *testing.T, pool *pgxpool.Pool, ctx context.Context, channelID int64, id int) {
	t.Helper()
	_, err := pool.Exec(ctx, `
INSERT INTO channel_messages (channel_id, id, random_id, sender_user_id, from_peer_id, message_date, body, pts)
VALUES ($1, $2, 0, (SELECT creator_user_id FROM channels WHERE id = $1), (SELECT creator_user_id FROM channels WHERE id = $1), 1700200001, $3, 1)`,
		channelID, id, fmt.Sprintf("backfilled %d", id))
	if err != nil {
		t.Fatalf("insert raw channel message %d: %v", id, err)
	}
}

func nextChannelMessageID(t *testing.T, allocator interface {
	NextChannelMessageID(context.Context, int64) (int, error)
}, channelID int64) int {
	t.Helper()
	id, err := allocator.NextChannelMessageID(context.Background(), channelID)
	if err != nil {
		t.Fatalf("allocate channel message id for %d: %v", channelID, err)
	}
	return id
}

// TestChannelMessageIDAllocatorPostgres pins the Postgres per-channel counter
// that replaced MAX(id)+1: ids continue after existing rows, survive a lost
// counter row, never hand the same id to two concurrent writers, and follow
// rows inserted without the allocator.
func TestChannelMessageIDAllocatorPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	allocator := NewChannelMessageIDAllocator(pool)

	t.Run("allocates consecutive ids after the channel creation message", func(t *testing.T) {
		channelID := newAllocatorTestChannel(t, pool, ctx)
		// CreateChannel writes one service message, so the first id handed out
		// here is expected to follow it.
		base, err := allocator.CurrentChannelMessageID(ctx, channelID)
		if err != nil {
			t.Fatalf("current id: %v", err)
		}
		if got := nextChannelMessageID(t, allocator, channelID); got != base+1 {
			t.Fatalf("first id = %d, want %d", got, base+1)
		}
		if got := nextChannelMessageID(t, allocator, channelID); got != base+2 {
			t.Fatalf("second id = %d, want %d", got, base+2)
		}
		// Allocation alone does not persist a message, so Current still reports
		// the highest stored row rather than the last handed-out id.
		current, err := allocator.CurrentChannelMessageID(ctx, channelID)
		if err != nil {
			t.Fatalf("current id: %v", err)
		}
		if current != base {
			t.Fatalf("current id = %d, want %d", current, base)
		}
	})

	t.Run("current reports the highest persisted id", func(t *testing.T) {
		channelID := newAllocatorTestChannel(t, pool, ctx)
		insertRawChannelMessage(t, pool, ctx, channelID, 5)
		current, err := allocator.CurrentChannelMessageID(ctx, channelID)
		if err != nil {
			t.Fatalf("current id: %v", err)
		}
		if current != 5 {
			t.Fatalf("current id = %d, want 5", current)
		}
	})

	t.Run("seeds from existing rows when the counter row is missing", func(t *testing.T) {
		channelID := newAllocatorTestChannel(t, pool, ctx)
		insertRawChannelMessage(t, pool, ctx, channelID, 7)
		if _, err := pool.Exec(ctx, `DELETE FROM channel_message_id_counters WHERE channel_id = $1`, channelID); err != nil {
			t.Fatalf("drop counter row: %v", err)
		}
		if got := nextChannelMessageID(t, allocator, channelID); got != 8 {
			t.Fatalf("id after a direct row insert with no counter = %d, want 8 (MAX(id)+1)", got)
		}
	})

	t.Run("never reuses ids of rows the counter missed", func(t *testing.T) {
		channelID := newAllocatorTestChannel(t, pool, ctx)
		insertRawChannelMessage(t, pool, ctx, channelID, 9)
		if _, err := pool.Exec(ctx, `
UPDATE channel_message_id_counters SET next_id = 2 WHERE channel_id = $1`, channelID); err != nil {
			t.Fatalf("rewind counter row: %v", err)
		}
		if got := nextChannelMessageID(t, allocator, channelID); got != 10 {
			t.Fatalf("id after a lagging counter = %d, want 10 (MAX(id)+1)", got)
		}
	})

	t.Run("never falls behind a counter that is already ahead", func(t *testing.T) {
		channelID := newAllocatorTestChannel(t, pool, ctx)
		if _, err := pool.Exec(ctx, `
INSERT INTO channel_message_id_counters (channel_id, next_id) VALUES ($1, 100)
ON CONFLICT (channel_id) DO UPDATE SET next_id = EXCLUDED.next_id`, channelID); err != nil {
			t.Fatalf("seed counter row: %v", err)
		}
		if got := nextChannelMessageID(t, allocator, channelID); got != 101 {
			t.Fatalf("id after an ahead counter = %d, want 101", got)
		}
	})

	t.Run("hands out distinct ids under concurrency", func(t *testing.T) {
		channelID := newAllocatorTestChannel(t, pool, ctx)
		const writers, perWriter = 16, 4
		base, err := allocator.CurrentChannelMessageID(ctx, channelID)
		if err != nil {
			t.Fatalf("current id: %v", err)
		}

		ids := make(chan int, writers*perWriter)
		errs := make(chan error, writers*perWriter)
		var wg sync.WaitGroup
		for range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range perWriter {
					id, err := allocator.NextChannelMessageID(ctx, channelID)
					if err != nil {
						errs <- err
						return
					}
					ids <- id
				}
			}()
		}
		wg.Wait()
		close(ids)
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent allocation: %v", err)
		}

		seen := make(map[int]struct{}, writers*perWriter)
		minID, maxID := 0, 0
		for id := range ids {
			if _, dup := seen[id]; dup {
				t.Fatalf("duplicate channel message id %d handed to two writers", id)
			}
			seen[id] = struct{}{}
			if minID == 0 || id < minID {
				minID = id
			}
			if id > maxID {
				maxID = id
			}
		}
		if len(seen) != writers*perWriter {
			t.Fatalf("allocated %d distinct ids, want %d", len(seen), writers*perWriter)
		}
		if minID != base+1 || maxID != base+writers*perWriter {
			t.Fatalf("allocated range [%d,%d], want [%d,%d]", minID, maxID, base+1, base+writers*perWriter)
		}
	})
}
