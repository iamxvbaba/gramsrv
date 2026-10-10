package redisstore

import (
	"context"
	"os"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// End-to-end through a real Redis: a tombstone written by the base-user cache
// must come back deleted. Before the flag was carried, this returned a live user
// with an empty name and LastSeenAt 0 -- the "last seen recently" stranger a
// client shows instead of Deleted Account.
func TestUserCacheServesTombstoneFromRedis(t *testing.T) {
	addr := os.Getenv("TELESRV_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TELESRV_TEST_REDIS_ADDR to run redis integration test")
	}
	ctx := context.Background()
	c, err := Open(ctx, addr, "", 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	const userID int64 = 77000011
	cache := NewUserCache(c, time.Minute)
	t.Cleanup(func() { _ = c.Del(ctx, userBaseKey(userID)).Err() })

	if err := cache.PutMany(ctx, []domain.User{{
		ID: userID, AccessHash: 7, FirstName: "Alice", LastSeenAt: 500,
	}}); err != nil {
		t.Fatalf("cache live user: %v", err)
	}
	live, err := cache.GetByIDs(ctx, []int64{userID})
	if err != nil {
		t.Fatalf("read live user: %v", err)
	}
	if u, ok := live[userID]; !ok || u.Deleted || u.FirstName != "Alice" || u.LastSeenAt != 500 {
		t.Fatalf("live cache read = %+v", live[userID])
	}

	// The deletion path replaces the cached entry with the tombstone projection.
	if err := cache.PutMany(ctx, []domain.User{{
		ID: userID, AccessHash: 7, Deleted: true, DeletedAt: 1_800_000_000,
	}}); err != nil {
		t.Fatalf("cache tombstone: %v", err)
	}
	got, err := cache.GetByIDs(ctx, []int64{userID})
	if err != nil {
		t.Fatalf("read tombstone: %v", err)
	}
	u, ok := got[userID]
	if !ok {
		t.Fatalf("tombstone missing from cache: %+v", got)
	}
	if !u.Deleted {
		t.Fatalf("cache served a live user for a deleted account: %+v", u)
	}
	if u.FirstName != "" || u.LastName != "" || u.LastSeenAt != 0 {
		t.Fatalf("cached tombstone leaked identity/presence: %+v", u)
	}
	if u.DeletedAt != 1_800_000_000 {
		t.Fatalf("cached tombstone deleted_at = %d", u.DeletedAt)
	}
}
