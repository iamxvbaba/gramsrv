package postgres

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestChannelRowCacheNilDisabled(t *testing.T) {
	if c := NewChannelRowCache(0); c != nil {
		t.Fatalf("max<=0 should return nil (disabled), got %v", c)
	}
	if c := NewChannelRowCache(-5); c != nil {
		t.Fatalf("negative capacity should return nil (disabled), got %v", c)
	}
	// All operations on a nil cache must be safe (no-op).
	var nilCache *ChannelRowCache
	nilCache.put(domain.Channel{ID: 1})
	nilCache.delete(1)
	nilCache.flush()
	if _, ok := nilCache.get(1); ok {
		t.Fatalf("get on a nil cache must return ok=false")
	}
}

func TestChannelRowCachePutGetDeleteFlush(t *testing.T) {
	c := NewChannelRowCache(16)
	ch := domain.Channel{ID: 42, Title: "hello", AccessHash: 7}

	if _, ok := c.get(42); ok {
		t.Fatalf("empty cache should not hit")
	}
	c.put(ch)
	got, ok := c.get(42)
	if !ok || got.ID != 42 || got.Title != "hello" || got.AccessHash != 7 {
		t.Fatalf("put/get round-trip failed: %+v ok=%v", got, ok)
	}

	c.delete(42)
	if _, ok := c.get(42); ok {
		t.Fatalf("should not hit after delete")
	}

	c.put(ch)
	c.flush()
	if _, ok := c.get(42); ok {
		t.Fatalf("should not hit after flush")
	}

	// id==0 is neither stored nor looked up.
	c.put(domain.Channel{ID: 0, Title: "zero"})
	if _, ok := c.get(0); ok {
		t.Fatalf("id==0 should not be cached/hit")
	}
}

func TestChannelRowCacheReturnsIsolatedClone(t *testing.T) {
	c := NewChannelRowCache(8)
	orig := domain.Channel{
		ID:            9,
		PhotoStripped: []byte{1, 2, 3},
	}
	orig.ReactionPolicy.Emoticons = []string{"👍"}
	orig.ReactionPolicy.CustomEmojiIDs = []int64{100}
	c.put(orig)

	// Mutating the underlying slice of the put argument must not pollute the cache (put must keep its own copy).
	orig.PhotoStripped[0] = 99
	orig.ReactionPolicy.Emoticons[0] = "💩"
	orig.ReactionPolicy.CustomEmojiIDs[0] = 999

	got, ok := c.get(9)
	if !ok {
		t.Fatalf("should hit")
	}
	if got.PhotoStripped[0] != 1 || got.ReactionPolicy.Emoticons[0] != "👍" || got.ReactionPolicy.CustomEmojiIDs[0] != 100 {
		t.Fatalf("cache polluted by later mutation of the put argument: %+v", got)
	}

	// Mutating the slice returned by get must not pollute the cache either (get must return a copy).
	got.PhotoStripped[0] = 77
	got.ReactionPolicy.Emoticons[0] = "🔥"
	got.ReactionPolicy.CustomEmojiIDs[0] = 777
	again, _ := c.get(9)
	if again.PhotoStripped[0] != 1 || again.ReactionPolicy.Emoticons[0] != "👍" || again.ReactionPolicy.CustomEmojiIDs[0] != 100 {
		t.Fatalf("cache polluted by mutation of the get return value: %+v", again)
	}
}

func TestChannelRowCacheCapEviction(t *testing.T) {
	c := NewChannelRowCache(2)
	c.put(domain.Channel{ID: 1})
	c.put(domain.Channel{ID: 2})
	// A third new id exceeds cap=2: LRU evicts the oldest single entry id1 rather than clearing the whole table.
	c.put(domain.Channel{ID: 3})
	if _, ok := c.get(1); ok {
		t.Fatalf("after overflow the oldest entry id1 should be evicted")
	}
	if _, ok := c.get(2); !ok {
		t.Fatalf("per-entry LRU eviction should keep the next-newest entry id2, proving it is not a whole-table flush")
	}
	if _, ok := c.get(3); !ok {
		t.Fatalf("the newest written entry id3 should be present")
	}
	// Updating an existing id takes effect in place and does not trigger eviction.
	c.put(domain.Channel{ID: 3, Title: "v2"})
	if got, ok := c.get(3); !ok || got.Title != "v2" {
		t.Fatalf("update of an existing id should take effect in place: %+v ok=%v", got, ok)
	}
}

func TestChannelRowCacheSingleflightsColdLoad(t *testing.T) {
	c := NewChannelRowCache(16)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	load := func() (domain.Channel, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return domain.Channel{ID: 42, Title: "loaded"}, nil
	}

	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ch, err := c.getOrLoad(context.Background(), 42, load)
			if err != nil {
				errs <- err
				return
			}
			if ch.ID != 42 || ch.Title != "loaded" {
				errs <- fmt.Errorf("channel = %+v, want loaded 42", ch)
			}
		}()
	}
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("load calls = %d, want 1", got)
	}
	if _, err := c.getOrLoad(context.Background(), 42, load); err != nil {
		t.Fatalf("cached getOrLoad: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("cache hit called load again: calls=%d", got)
	}
}

func TestChannelRowCacheBatchesMissesAndNegativeRows(t *testing.T) {
	c := NewChannelRowCache(8)
	loads := 0
	load := func(_ context.Context, ids []int64) (map[int64]domain.Channel, error) {
		loads++
		out := make(map[int64]domain.Channel, len(ids))
		for _, id := range ids {
			if id != 2 {
				out[id] = domain.Channel{ID: id, Title: "channel"}
			}
		}
		return out, nil
	}
	for i := 0; i < 2; i++ {
		got, err := c.getOrLoadBatch(context.Background(), []int64{1, 2, 3}, load)
		if err != nil {
			t.Fatal(err)
		}
		if got[1].ID != 1 || got[2].ID != 0 || got[3].ID != 3 {
			t.Fatalf("batch result = %+v", got)
		}
	}
	if loads != 1 {
		t.Fatalf("batch loads = %d, want 1", loads)
	}
}
