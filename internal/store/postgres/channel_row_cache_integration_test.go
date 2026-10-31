package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestChannelRowCacheInvalidatesOnNotify verifies end to end: channels trigger (0117) →
// LISTEN/NOTIFY → ChannelChangeListener → ChannelRowCache invalidation loop. Gated on
// TELESRV_TEST_POSTGRES_DSN.
//
// It uses "the flush on listener connect" as the readiness signal (polling until the cache is
// cleared confirms the listener has LISTENed), avoiding races from guessing timing with sleep.
func TestChannelRowCacheInvalidatesOnNotify(t *testing.T) {
	pool := testPool(t) // without a DSN set this will t.Skip
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 451,
		Phone:      "+1889" + suffix + "01",
		FirstName:  "CacheOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}

	cache := NewChannelRowCache(1000)
	channels := NewChannelStore(pool, WithChannelRowCache(cache))

	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "CacheChan " + suffix,
		Megagroup:     true,
		Date:          1700000700,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	// Fill the cache first (the listener is not running yet).
	if _, err := channels.GetChannel(ctx, owner.ID, channelID); err != nil {
		t.Fatalf("warm GetChannel: %v", err)
	}
	if _, ok := cache.get(channelID); !ok {
		t.Fatalf("the channel row should be cached after GetChannel")
	}

	// Start the listener; on connect it flushes the whole table — use the cache being cleared as the "listener is ready" signal.
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	listener := NewChannelChangeListener(dsn, cache, nil)
	go listener.Run(lctx)
	if !waitUntil(2*time.Second, func() bool { _, ok := cache.get(channelID); return !ok }) {
		t.Fatalf("the listener did not connect and flush the cache within the expected time")
	}

	// Re-fill the cache once the listener is ready.
	if _, err := channels.GetChannel(ctx, owner.ID, channelID); err != nil {
		t.Fatalf("re-warm GetChannel: %v", err)
	}
	if _, ok := cache.get(channelID); !ok {
		t.Fatalf("the channel row should be cached after re-warm")
	}

	// Simulate an arbitrary write point changing the channels table directly (the trigger will NOTIFY).
	const newTitle = "Renamed By Trigger"
	if _, err := pool.Exec(ctx, "UPDATE channels SET title = $2, updated_at = now() WHERE id = $1", channelID, newTitle); err != nil {
		t.Fatalf("update title: %v", err)
	}

	// Trigger → NOTIFY → the listener should invalidate that channel in real time.
	if !waitUntil(3*time.Second, func() bool { _, ok := cache.get(channelID); return !ok }) {
		t.Fatalf("the cache was not invalidated by NOTIFY within the expected time after UPDATE")
	}

	// After invalidation, a read should get the new value (and re-fill the cache).
	view, err := channels.GetChannel(ctx, owner.ID, channelID)
	if err != nil {
		t.Fatalf("post-invalidation GetChannel: %v", err)
	}
	if view.Channel.Title != newTitle {
		t.Fatalf("title after invalidation = %q, want %q", view.Channel.Title, newTitle)
	}
}

func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}
