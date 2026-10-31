package readmodelcache

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock advances TTL behavior deterministically without sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func TestGetOrLoadSingleflightsConcurrentMiss(t *testing.T) {
	ctx := context.Background()
	c := New[int, string](Config[int, string]{MaxEntries: 16})

	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	load := func() (string, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return "v", nil
	}

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			v, err := c.GetOrLoad(ctx, 42, load)
			if err != nil {
				errs <- err
				return
			}
			if v != "v" {
				errs <- fmt.Errorf("value = %q, want v", v)
			}
		}()
	}
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("load called %d times, want 1 (singleflight)", got)
	}
	// Hit: no further load.
	if v, err := c.GetOrLoad(ctx, 42, func() (string, error) { return "miss", nil }); err != nil || v != "v" {
		t.Fatalf("cached GetOrLoad = %q,%v want v,<nil>", v, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("cache hit re-loaded: calls=%d", got)
	}
}

func TestInvalidateWhereValueUsesImmutableDependency(t *testing.T) {
	type value struct{ channels []int64 }
	c := New[int, value](Config[int, value]{MaxEntries: 4})
	c.Store(1, value{channels: []int64{7, 8}})
	c.Store(2, value{channels: []int64{9}})

	c.InvalidateWhereValue(func(_ int, v value) bool {
		for _, id := range v.channels {
			if id == 8 {
				return true
			}
		}
		return false
	})
	if _, ok := c.Peek(1); ok {
		t.Fatal("dependency match remained cached")
	}
	if got, ok := c.Peek(2); !ok || len(got.channels) != 1 || got.channels[0] != 9 {
		t.Fatalf("unrelated value = %+v,%v, want cached channel 9", got, ok)
	}
}

// TestEpochGuardRejectsStaleWriteback proves the epoch guard closes a lost-update:
// an Invalidate arriving during an out-of-lock load must not be overwritten by
// this load's (now stale) result; the in-flight reader ultimately gets the value
// reloaded after the invalidation, and the cache is not poisoned with stale data.
func TestEpochGuardRejectsStaleWriteback(t *testing.T) {
	ctx := context.Background()
	c := New[int, string](Config[int, string]{MaxEntries: 16})

	var seq atomic.Int32 // first load returns stale, the second (after invalidation) returns fresh
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	load := func() (string, error) {
		n := seq.Add(1)
		if n == 1 {
			once.Do(func() { close(started) })
			<-release // the first load blocks, simulating spanning an invalidation
			return "stale", nil
		}
		return "fresh", nil
	}

	resCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		v, err := c.GetOrLoad(ctx, 7, load)
		if err != nil {
			errCh <- err
			return
		}
		resCh <- v
	}()

	<-started
	// The invalidation arrives during the load (the cache has no key 7 yet, so the
	// delete is a no-op, but epoch++).
	c.Invalidate(7)
	close(release) // release the first load: it returns "stale", and the epoch guard must refuse the write-back.

	select {
	case err := <-errCh:
		t.Fatalf("GetOrLoad: %v", err)
	case v := <-resCh:
		if v != "fresh" {
			t.Fatalf("in-flight reader got %q, want fresh (stale must not win)", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for in-flight reader")
	}

	// The cache should now hold fresh, and subsequent reads should not load again.
	callsBefore := seq.Load()
	if v, err := c.GetOrLoad(ctx, 7, load); err != nil || v != "fresh" {
		t.Fatalf("post-race GetOrLoad = %q,%v want fresh,<nil>", v, err)
	}
	if seq.Load() != callsBefore {
		t.Fatalf("cache poisoned/evicted: reloaded (seq %d -> %d)", callsBefore, seq.Load())
	}
}

func TestLRUEvictsOldestNotWholeMap(t *testing.T) {
	c := New[int, int](Config[int, int]{MaxEntries: 3})
	for i := 1; i <= 3; i++ {
		mustLoad(t, c, i, i*10)
	}
	// Insert the 4th: only the oldest (key 1) should be evicted, not the whole table cleared.
	mustLoad(t, c, 4, 40)
	if c.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (single-entry eviction, not whole-map flush)", c.Len())
	}
	if _, ok := c.Peek(1); ok {
		t.Fatal("key 1 should have been evicted as oldest")
	}
	for _, k := range []int{2, 3, 4} {
		if v, ok := c.Peek(k); !ok || v != k*10 {
			t.Fatalf("key %d evicted/lost (whole-map flush?): v=%d ok=%v", k, v, ok)
		}
	}
}

func TestLRUTouchOnGet(t *testing.T) {
	ctx := context.Background()
	c := New[int, int](Config[int, int]{MaxEntries: 2})
	mustLoad(t, c, 1, 10)
	mustLoad(t, c, 2, 20)
	// Touch key 1 to make it the most recently used.
	if v, err := c.GetOrLoad(ctx, 1, failLoad[int](t)); err != nil || v != 10 {
		t.Fatalf("touch GetOrLoad(1) = %d,%v", v, err)
	}
	// Insert key 3: key 2 (least recently used) should be evicted, keeping key 1.
	mustLoad(t, c, 3, 30)
	if _, ok := c.Peek(2); ok {
		t.Fatal("key 2 should have been evicted (LRU), key 1 was touched")
	}
	if _, ok := c.Peek(1); !ok {
		t.Fatal("key 1 was touched and must survive")
	}
}

func TestWeightedLRUEvictsByTotalWeightAndSkipsOversize(t *testing.T) {
	ctx := context.Background()
	c := New[int, int](Config[int, int]{
		MaxEntries: 10,
		MaxWeight:  5,
		Weight:     func(v int) int64 { return int64(v) },
	})
	mustLoad(t, c, 1, 2)
	mustLoad(t, c, 2, 2)
	mustLoad(t, c, 3, 3)
	if _, ok := c.Peek(1); ok {
		t.Fatal("oldest entry should be evicted when aggregate weight exceeds five")
	}
	for _, key := range []int{2, 3} {
		if _, ok := c.Peek(key); !ok {
			t.Fatalf("weighted LRU lost retained key %d", key)
		}
	}
	loads := 0
	loadOversize := func() (int, error) { loads++; return 6, nil }
	if v, err := c.GetOrLoad(ctx, 4, loadOversize); err != nil || v != 6 {
		t.Fatalf("oversize first load = %d,%v", v, err)
	}
	if v, err := c.GetOrLoad(ctx, 4, loadOversize); err != nil || v != 6 {
		t.Fatalf("oversize second load = %d,%v", v, err)
	}
	if loads != 2 {
		t.Fatalf("oversize value unexpectedly retained: loads=%d, want 2", loads)
	}
	if _, ok := c.Peek(4); ok {
		t.Fatal("single value above MaxWeight must not remain cached")
	}
}

func TestLifecycleCallbacksCoverReplaceEvictAndFlush(t *testing.T) {
	type event struct {
		op    string
		key   int
		value string
	}
	var events []event
	c := New[int, string](Config[int, string]{
		MaxEntries: 2,
		OnStore: func(key int, value string) {
			events = append(events, event{op: "store", key: key, value: value})
		},
		OnRemove: func(key int, value string) {
			events = append(events, event{op: "remove", key: key, value: value})
		},
	})
	c.Store(1, "a")
	c.Store(1, "b")
	c.Store(2, "c")
	c.Store(3, "d")
	c.Flush()
	want := []event{
		{op: "store", key: 1, value: "a"},
		{op: "remove", key: 1, value: "a"},
		{op: "store", key: 1, value: "b"},
		{op: "store", key: 2, value: "c"},
		{op: "store", key: 3, value: "d"},
		{op: "remove", key: 1, value: "b"},
		{op: "remove", key: 3, value: "d"},
		{op: "remove", key: 2, value: "c"},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle events = %#v, want %#v", events, want)
	}
}

func TestVersionGateReloadsOnHashChange(t *testing.T) {
	ctx := context.Background()
	c := New[int, string](Config[int, string]{MaxEntries: 16})

	var calls atomic.Int32
	load := func(tag string) func() (string, error) {
		return func() (string, error) { calls.Add(1); return tag, nil }
	}
	// Insert under hash 100.
	if v, _ := c.GetOrLoadVersioned(ctx, 1, 100, load("h100")); v != "h100" {
		t.Fatalf("first load = %q", v)
	}
	// Same hash: hit, no load.
	if v, _ := c.GetOrLoadVersioned(ctx, 1, 100, failLoad[string](t)); v != "h100" {
		t.Fatalf("same-hash hit = %q", v)
	}
	if calls.Load() != 1 {
		t.Fatalf("same-hash re-loaded: calls=%d", calls.Load())
	}
	// Hash changed: miss, reload.
	if v, _ := c.GetOrLoadVersioned(ctx, 1, 200, load("h200")); v != "h200" {
		t.Fatalf("hash-change reload = %q", v)
	}
	if calls.Load() != 2 {
		t.Fatalf("hash change should reload once more: calls=%d", calls.Load())
	}
}

func TestTTLExpiryIsPerKeyAndDoesNotBumpEpoch(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	c := New[int, int](Config[int, int]{MaxEntries: 16, TTL: time.Minute})
	c.now = clock.now

	mustLoad(t, c, 1, 10)
	epochBefore := c.cacheEpoch()

	clock.advance(2 * time.Minute) // past the TTL

	var reloaded atomic.Int32
	v, err := c.GetOrLoad(ctx, 1, func() (int, error) { reloaded.Add(1); return 11, nil })
	if err != nil || v != 11 {
		t.Fatalf("expired GetOrLoad = %d,%v want 11", v, err)
	}
	if reloaded.Load() != 1 {
		t.Fatal("expired entry should have reloaded")
	}
	if c.cacheEpoch() != epochBefore {
		t.Fatalf("TTL expiry bumped epoch %d -> %d (would thrash in-flight loads)", epochBefore, c.cacheEpoch())
	}
}

func TestInvalidateWhereFanout(t *testing.T) {
	ctx := context.Background()
	type key struct{ channel, user int64 }
	c := New[key, int](Config[key, int]{MaxEntries: 64})
	put := func(ch, u int64) {
		if _, err := c.GetOrLoad(ctx, key{ch, u}, func() (int, error) { return 1, nil }); err != nil {
			t.Fatal(err)
		}
	}
	put(10, 1)
	put(10, 2)
	put(20, 1)
	epochBefore := c.cacheEpoch()

	c.InvalidateWhere(func(k key) bool { return k.channel == 10 })

	if _, ok := c.Peek(key{10, 1}); ok {
		t.Fatal("channel 10 user 1 should be invalidated")
	}
	if _, ok := c.Peek(key{10, 2}); ok {
		t.Fatal("channel 10 user 2 should be invalidated")
	}
	if _, ok := c.Peek(key{20, 1}); !ok {
		t.Fatal("channel 20 must survive a channel-10 fanout invalidate")
	}
	if c.cacheEpoch() == epochBefore {
		t.Fatal("InvalidateWhere must bump epoch")
	}
}

func TestCloneIsolatesCallerFromCache(t *testing.T) {
	ctx := context.Background()
	c := New[int, []int](Config[int, []int]{
		MaxEntries: 16,
		Clone:      func(v []int) []int { return append([]int(nil), v...) },
	})
	orig := []int{1, 2, 3}
	got, err := c.GetOrLoad(ctx, 1, func() ([]int, error) { return orig, nil })
	if err != nil {
		t.Fatal(err)
	}
	got[0] = 999  // mutate the returned value
	orig[1] = 888 // mutate the load source
	again, _ := c.Peek(1)
	if again[0] != 1 || again[1] != 2 || again[2] != 3 {
		t.Fatalf("cache entry mutated through aliasing: %v", again)
	}
}

func TestFlushClearsAndBumpsEpoch(t *testing.T) {
	ctx := context.Background()
	c := New[int, int](Config[int, int]{MaxEntries: 16})
	for i := 0; i < 5; i++ {
		mustLoad(t, c, i, i)
	}
	epochBefore := c.cacheEpoch()
	c.Flush()
	if c.Len() != 0 {
		t.Fatalf("Len after flush = %d, want 0", c.Len())
	}
	if c.cacheEpoch() == epochBefore {
		t.Fatal("Flush must bump epoch")
	}
	_ = ctx
}

func TestNilCacheBypassesToLoad(t *testing.T) {
	ctx := context.Background()
	var c *Cache[int, string] // New returns nil when MaxEntries<=0
	if got := New[int, string](Config[int, string]{MaxEntries: 0}); got != nil {
		t.Fatalf("New(MaxEntries:0) = %v, want nil", got)
	}
	var calls atomic.Int32
	v, err := c.GetOrLoad(ctx, 1, func() (string, error) { calls.Add(1); return "x", nil })
	if err != nil || v != "x" {
		t.Fatalf("nil cache GetOrLoad = %q,%v", v, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("nil cache should call load exactly once: %d", calls.Load())
	}
	// Other methods are nil-safe.
	c.Invalidate(1)
	c.InvalidateWhere(func(int) bool { return true })
	c.Flush()
	c.Store(1, "y")
	if _, ok := c.Peek(1); ok {
		t.Fatal("nil cache Peek must miss")
	}
	if c.Len() != 0 {
		t.Fatal("nil cache Len must be 0")
	}
}

func TestStoreIfEpochRejectsStaleExternalBuild(t *testing.T) {
	// External-build mode: LoadEpoch snapshot -> invalidation during the build ->
	// StoreIfEpoch must refuse the write-back.
	c := New[int, string](Config[int, string]{MaxEntries: 16})
	loadEpoch := c.LoadEpoch()
	c.Invalidate(7) // invalidation during the build (epoch++)
	c.StoreIfEpoch(7, "stale", loadEpoch)
	if _, ok := c.Peek(7); ok {
		t.Fatal("StoreIfEpoch must refuse the write-back after the epoch changed")
	}
	// With no concurrent invalidation, StoreIfEpoch writes normally.
	fresh := c.LoadEpoch()
	c.StoreIfEpoch(7, "fresh", fresh)
	if v, ok := c.Peek(7); !ok || v != "fresh" {
		t.Fatalf("StoreIfEpoch should write when the epoch is unchanged: %q ok=%v", v, ok)
	}
}

// batchVal models a value with negative caching (found=false means "looked up but does not exist").
type batchVal struct {
	n     int
	found bool
}

func TestGetOrLoadBatchCachesHitsMissesAndNegatives(t *testing.T) {
	ctx := context.Background()
	c := New[int, batchVal](Config[int, batchVal]{MaxEntries: 64})
	noVersion := func(int) (int64, bool) { return 0, true }

	var loadCalls atomic.Int32
	var lastMissing atomic.Int32
	load := func(_ context.Context, missing []int) (map[int]batchVal, error) {
		loadCalls.Add(1)
		lastMissing.Store(int32(len(missing)))
		out := make(map[int]batchVal, len(missing))
		for _, k := range missing {
			// Even keys exist, odd ones are negative results (still returned so the negative result can be cached).
			out[k] = batchVal{n: k * 10, found: k%2 == 0}
		}
		return out, nil
	}

	// First pass: all miss, one batch load.
	got, err := c.GetOrLoadBatch(ctx, []int{1, 2, 3, 4}, noVersion, load)
	if err != nil {
		t.Fatal(err)
	}
	if got[2].n != 20 || !got[2].found || got[3].found {
		t.Fatalf("batch result wrong: %+v", got)
	}
	if loadCalls.Load() != 1 || lastMissing.Load() != 4 {
		t.Fatalf("first batch: calls=%d missing=%d, want 1/4", loadCalls.Load(), lastMissing.Load())
	}

	// Second lookup (including a new key 6): only 6 misses; negative results (1/3) are already cached and not loaded again.
	got2, err := c.GetOrLoadBatch(ctx, []int{1, 2, 3, 4, 6}, noVersion, load)
	if err != nil {
		t.Fatal(err)
	}
	if loadCalls.Load() != 2 || lastMissing.Load() != 1 {
		t.Fatalf("second batch should load only key 6: calls=%d missing=%d", loadCalls.Load(), lastMissing.Load())
	}
	if got2[6].n != 60 || !got2[6].found {
		t.Fatalf("key 6 = %+v, want 60/found", got2[6])
	}
}

func TestGetOrLoadBatchCoalescesOverlappingConcurrentMissesPerKey(t *testing.T) {
	ctx := context.Background()
	c := New[int, batchVal](Config[int, batchVal]{MaxEntries: 64})
	noVersion := func(int) (int64, bool) { return 0, true }

	firstStarted := make(chan struct{})
	secondLoaded := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	var mu sync.Mutex
	loadedKeys := make(map[int]int)
	load := func(_ context.Context, missing []int) (map[int]batchVal, error) {
		call := calls.Add(1)
		mu.Lock()
		for _, key := range missing {
			loadedKeys[key]++
		}
		mu.Unlock()
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
		} else {
			close(secondLoaded)
		}
		out := make(map[int]batchVal, len(missing))
		for _, key := range missing {
			out[key] = batchVal{n: key * 10, found: true}
		}
		return out, nil
	}

	firstResult := make(chan map[int]batchVal, 1)
	firstErr := make(chan error, 1)
	go func() {
		got, err := c.GetOrLoadBatch(ctx, []int{1, 2, 3}, noVersion, load)
		firstResult <- got
		firstErr <- err
	}()
	<-firstStarted

	secondResult := make(chan map[int]batchVal, 1)
	secondErr := make(chan error, 1)
	go func() {
		got, err := c.GetOrLoadBatch(ctx, []int{2, 3, 4}, noVersion, load)
		secondResult <- got
		secondErr <- err
	}()
	<-secondLoaded
	close(releaseFirst)

	first, second := <-firstResult, <-secondResult
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
	if err := <-secondErr; err != nil {
		t.Fatal(err)
	}
	if first[1].n != 10 || first[2].n != 20 || first[3].n != 30 ||
		second[2].n != 20 || second[3].n != 30 || second[4].n != 40 {
		t.Fatalf("overlapping results first=%+v second=%+v", first, second)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls.Load() != 2 || loadedKeys[1] != 1 || loadedKeys[2] != 1 || loadedKeys[3] != 1 || loadedKeys[4] != 1 {
		t.Fatalf("backend calls=%d loaded=%v, want two batches and every key exactly once", calls.Load(), loadedKeys)
	}
}

func TestGetOrLoadBatchVersionGateReloadsOnHashChange(t *testing.T) {
	ctx := context.Background()
	c := New[int, batchVal](Config[int, batchVal]{MaxEntries: 64})
	var hash atomic.Int64
	hash.Store(100)
	versionOf := func(int) (int64, bool) { return hash.Load(), true }

	var loadCalls atomic.Int32
	load := func(_ context.Context, missing []int) (map[int]batchVal, error) {
		loadCalls.Add(1)
		out := make(map[int]batchVal, len(missing))
		for _, k := range missing {
			out[k] = batchVal{n: int(hash.Load()), found: true}
		}
		return out, nil
	}
	if _, err := c.GetOrLoadBatch(ctx, []int{1}, versionOf, load); err != nil {
		t.Fatal(err)
	}
	// Same hash: hit, no load.
	if _, err := c.GetOrLoadBatch(ctx, []int{1}, versionOf, load); err != nil {
		t.Fatal(err)
	}
	if loadCalls.Load() != 1 {
		t.Fatalf("same-hash batch re-loaded: calls=%d", loadCalls.Load())
	}
	// Hash changed: version gate misses, reload.
	hash.Store(200)
	got, err := c.GetOrLoadBatch(ctx, []int{1}, versionOf, load)
	if err != nil {
		t.Fatal(err)
	}
	if loadCalls.Load() != 2 || got[1].n != 200 {
		t.Fatalf("hash change should reload: calls=%d got=%+v", loadCalls.Load(), got[1])
	}
}

// TestGetOrLoadBatchStoresUnderLookupHashNotStoreHash pins down the version-gate
// fix: the write-back must use the hash snapshotted at lookup time, not the hash
// recomputed at write-back. Here loadMissing (which runs exactly between the
// lookup and the write) changes the version from 100 to 200 -- if the write-back
// wrongly used the new hash 200, data loaded under version 100 would be hit as
// new data for 200.
func TestGetOrLoadBatchStoresUnderLookupHashNotStoreHash(t *testing.T) {
	ctx := context.Background()
	c := New[int, batchVal](Config[int, batchVal]{MaxEntries: 64})
	var gen atomic.Int64
	gen.Store(100)
	versionOf := func(int) (int64, bool) { return gen.Load(), true }

	var loadCalls atomic.Int32
	load := func(_ context.Context, missing []int) (map[int]batchVal, error) {
		loadCalls.Add(1)
		gen.Store(200) // version changes between the lookup (@100, miss) and the write-back
		out := make(map[int]batchVal, len(missing))
		for _, k := range missing {
			out[k] = batchVal{n: k, found: true}
		}
		return out, nil
	}

	// First pass: lookup@100 miss -> load (changes gen to 200) -> write-back must use the snapshotted 100, not 200.
	if _, err := c.GetOrLoadBatch(ctx, []int{1}, versionOf, load); err != nil {
		t.Fatal(err)
	}
	if loadCalls.Load() != 1 {
		t.Fatalf("first batch load calls = %d, want 1", loadCalls.Load())
	}
	// Now currentHash=200. If the entry were wrongly stamped with 200 (bug), this
	// read would hit stale data and not reload; after the fix the entry is
	// 100 != 200 -> miss -> reload.
	if _, err := c.GetOrLoadBatch(ctx, []int{1}, versionOf, load); err != nil {
		t.Fatal(err)
	}
	if loadCalls.Load() != 2 {
		t.Fatalf("version changed during first load; second read must reload (loadCalls=%d, want 2) — stale-as-fresh version-gate bypass", loadCalls.Load())
	}
}

func TestGetOrLoadBatchBypassesNonCacheable(t *testing.T) {
	ctx := context.Background()
	c := New[int, batchVal](Config[int, batchVal]{MaxEntries: 64})
	// key 7 is not cacheable (missing version), the rest are pure TTL caches.
	versionOf := func(k int) (int64, bool) { return 0, k != 7 }
	var loadCalls atomic.Int32
	load := func(_ context.Context, missing []int) (map[int]batchVal, error) {
		loadCalls.Add(1)
		out := make(map[int]batchVal, len(missing))
		for _, k := range missing {
			out[k] = batchVal{n: k, found: true}
		}
		return out, nil
	}
	if _, err := c.GetOrLoadBatch(ctx, []int{7, 8}, versionOf, load); err != nil {
		t.Fatal(err)
	}
	// 7 is not cacheable and was not written back; 8 is cached. The next lookup should load only 7.
	got, err := c.GetOrLoadBatch(ctx, []int{7, 8}, versionOf, load)
	if err != nil {
		t.Fatal(err)
	}
	if loadCalls.Load() != 2 {
		t.Fatalf("non-cacheable key 7 should reload every time: calls=%d", loadCalls.Load())
	}
	if _, ok := c.Peek(7); ok {
		t.Fatal("non-cacheable key 7 must not be stored")
	}
	if got[8].n != 8 {
		t.Fatalf("key 8 = %+v", got[8])
	}
}

func TestGetOrLoadBatchRetriesOnEpochChangeDuringLoad(t *testing.T) {
	ctx := context.Background()
	c := New[int, batchVal](Config[int, batchVal]{MaxEntries: 64})
	noVersion := func(int) (int64, bool) { return 0, true }

	var seq atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	load := func(_ context.Context, missing []int) (map[int]batchVal, error) {
		n := seq.Add(1)
		out := make(map[int]batchVal, len(missing))
		if n == 1 {
			once.Do(func() { close(started) })
			<-release // the first batch load blocks, an invalidation happens meanwhile
			for _, k := range missing {
				out[k] = batchVal{n: k, found: true} // old batch data
			}
			return out, nil
		}
		for _, k := range missing {
			out[k] = batchVal{n: k + 1000, found: true} // reloaded data after the invalidation
		}
		return out, nil
	}

	resCh := make(chan map[int]batchVal, 1)
	go func() {
		got, _ := c.GetOrLoadBatch(ctx, []int{1}, noVersion, load)
		resCh <- got
	}()
	<-started
	c.Invalidate(1) // invalidation during the batch load -> epoch++
	close(release)

	got := <-resCh
	if got[1].n != 1001 {
		t.Fatalf("epoch retry should return reloaded data: got %+v, want 1001", got[1])
	}
	// The cache should hold the reloaded value, and further reads should not load.
	callsBefore := seq.Load()
	if v, ok := c.Peek(1); !ok || v.n != 1001 {
		t.Fatalf("cache should hold reloaded value: %+v ok=%v", v, ok)
	}
	if seq.Load() != callsBefore {
		t.Fatalf("post-retry read hit loader: seq %d", seq.Load())
	}
}

func TestStoreWarmFromListDoesNotBumpEpoch(t *testing.T) {
	c := New[int, int](Config[int, int]{MaxEntries: 16})
	epochBefore := c.cacheEpoch()
	c.Store(1, 10)
	if c.cacheEpoch() != epochBefore {
		t.Fatal("Store (warm) must not bump epoch")
	}
	if v, ok := c.Peek(1); !ok || v != 10 {
		t.Fatalf("Store then Peek = %d,%v", v, ok)
	}
}

func TestLoadErrorNotCached(t *testing.T) {
	ctx := context.Background()
	c := New[int, int](Config[int, int]{MaxEntries: 16})
	wantErr := fmt.Errorf("boom")
	if _, err := c.GetOrLoad(ctx, 1, func() (int, error) { return 0, wantErr }); err != wantErr {
		t.Fatalf("err = %v, want boom", err)
	}
	if _, ok := c.Peek(1); ok {
		t.Fatal("failed load must not be cached")
	}
	// A later successful load should take effect.
	if v, err := c.GetOrLoad(ctx, 1, func() (int, error) { return 5, nil }); err != nil || v != 5 {
		t.Fatalf("retry GetOrLoad = %d,%v", v, err)
	}
}

// TestConcurrentAccessNoPanic catches data races under -race; without -race it verifies there is no panic/deadlock.
func TestConcurrentAccessNoPanic(t *testing.T) {
	ctx := context.Background()
	c := New[int, int](Config[int, int]{MaxEntries: 64, TTL: time.Millisecond})
	var wg sync.WaitGroup
	for g := 0; g < 24; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				k := (g + i) % 128
				switch i % 5 {
				case 0:
					_, _ = c.GetOrLoad(ctx, k, func() (int, error) { return k, nil })
				case 1:
					_, _ = c.GetOrLoadVersioned(ctx, k, int64(i%7+1), func() (int, error) { return k, nil })
				case 2:
					c.Invalidate(k)
				case 3:
					c.InvalidateWhere(func(x int) bool { return x%3 == 0 })
				case 4:
					c.Store(k, k)
				}
			}
		}(g)
	}
	wg.Wait()
}

// --- helpers ---

func mustLoad[K comparable, V comparable](t *testing.T, c *Cache[K, V], key K, val V) {
	t.Helper()
	v, err := c.GetOrLoad(context.Background(), key, func() (V, error) { return val, nil })
	if err != nil {
		t.Fatalf("GetOrLoad(%v): %v", key, err)
	}
	if v != val {
		t.Fatalf("GetOrLoad(%v) = %v, want %v", key, v, val)
	}
}

func failLoad[V any](t *testing.T) func() (V, error) {
	return func() (V, error) {
		t.Helper()
		t.Fatal("load should not have been called (expected cache hit)")
		var zero V
		return zero, nil
	}
}
