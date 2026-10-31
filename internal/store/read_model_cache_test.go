package store

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
)

type blockingReadModelVersionStore struct {
	calls   atomic.Int32
	once    sync.Once
	started chan struct{}
	release chan struct{}
	hashes  map[ReadModelKey]int64
	mu      sync.Mutex
	loaded  map[ReadModelKey]int
}

func (s *blockingReadModelVersionStore) ReadModelHash(ctx context.Context, model string, ownerUserID int64, peerType domain.PeerType, peerID int64) (int64, bool, error) {
	rows, err := s.ReadModelHashes(ctx, []ReadModelKey{{Model: model, OwnerUserID: ownerUserID, PeerType: peerType, PeerID: peerID}})
	if err != nil {
		return 0, false, err
	}
	key := ReadModelKey{Model: model, OwnerUserID: ownerUserID, PeerType: peerType, PeerID: peerID}
	hash := rows[key]
	return hash, hash != 0, nil
}

func (s *blockingReadModelVersionStore) ReadModelHashes(ctx context.Context, keys []ReadModelKey) (map[ReadModelKey]int64, error) {
	s.calls.Add(1)
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make(map[ReadModelKey]int64, len(keys))
	s.mu.Lock()
	if s.loaded == nil {
		s.loaded = make(map[ReadModelKey]int)
	}
	for _, key := range keys {
		s.loaded[key]++
		if hash := s.hashes[key]; hash != 0 {
			out[key] = hash
		}
	}
	s.mu.Unlock()
	return out, nil
}

func TestCachedReadModelVersionStoreSingleflightsBatchMiss(t *testing.T) {
	ctx := context.Background()
	keys := []ReadModelKey{
		{Model: "channel_base", OwnerUserID: 0, PeerType: domain.PeerTypeChannel, PeerID: 10},
		{Model: "channel_member", OwnerUserID: 100, PeerType: domain.PeerTypeChannel, PeerID: 10},
	}
	base := &blockingReadModelVersionStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
		hashes: map[ReadModelKey]int64{
			keys[0]: 11,
			keys[1]: 22,
		},
	}
	cache := NewCachedReadModelVersionStore(base, time.Hour, 100)

	const goroutines = 16
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			rows, err := cache.ReadModelHashes(ctx, keys)
			if err != nil {
				errs <- err
				return
			}
			if rows[keys[0]] != 11 || rows[keys[1]] != 22 {
				errs <- fmt.Errorf("hashes = %+v, want 11/22", rows)
			}
		}()
	}
	<-base.started
	time.Sleep(20 * time.Millisecond)
	close(base.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := base.calls.Load(); got != 1 {
		t.Fatalf("base ReadModelHashes calls = %d, want 1", got)
	}
	if _, err := cache.ReadModelHashes(ctx, keys); err != nil {
		t.Fatalf("cached ReadModelHashes: %v", err)
	}
	if got := base.calls.Load(); got != 1 {
		t.Fatalf("cache hit called base again: calls=%d", got)
	}
}

func TestCachedReadModelVersionStoreWaitsOverlappingBatchKeys(t *testing.T) {
	ctx := context.Background()
	a := ReadModelKey{Model: "channel_base", OwnerUserID: 0, PeerType: domain.PeerTypeChannel, PeerID: 10}
	b := ReadModelKey{Model: "channel_member", OwnerUserID: 100, PeerType: domain.PeerTypeChannel, PeerID: 10}
	c := ReadModelKey{Model: "dialog_light", OwnerUserID: 100, PeerType: domain.PeerTypeChannel, PeerID: 10}
	base := &blockingReadModelVersionStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
		hashes: map[ReadModelKey]int64{
			a: 11,
			b: 22,
			c: 33,
		},
	}
	cache := NewCachedReadModelVersionStore(base, time.Hour, 100)

	firstErr := make(chan error, 1)
	go func() {
		rows, err := cache.ReadModelHashes(ctx, []ReadModelKey{a, b})
		if err == nil && (rows[a] != 11 || rows[b] != 22) {
			err = fmt.Errorf("first hashes = %+v, want A/B", rows)
		}
		firstErr <- err
	}()
	<-base.started

	secondErr := make(chan error, 1)
	go func() {
		rows, err := cache.ReadModelHashes(ctx, []ReadModelKey{b, c})
		if err == nil && (rows[b] != 22 || rows[c] != 33) {
			err = fmt.Errorf("second hashes = %+v, want B/C", rows)
		}
		secondErr <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(base.release)

	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
	if err := <-secondErr; err != nil {
		t.Fatal(err)
	}
	base.mu.Lock()
	loadedB := base.loaded[b]
	loadedC := base.loaded[c]
	base.mu.Unlock()
	if loadedB != 1 {
		t.Fatalf("overlap key B loaded %d times, want 1", loadedB)
	}
	if loadedC != 1 {
		t.Fatalf("owned key C loaded %d times, want 1", loadedC)
	}
}

// TestCachedReadModelVersionStoreEpochGuardRejectsStaleWriteback proves the epoch guard closes
// a lost-update: a NOTIFY (writing a new hash) that arrives during an out-of-lock DB load must
// not be overwritten by the stale hash the load returns. This is the root of correctness for the
// 4 hash-only consumer caches (private/channel media counts, participants, active ids).
func TestCachedReadModelVersionStoreEpochGuardRejectsStaleWriteback(t *testing.T) {
	ctx := context.Background()
	key := ReadModelKey{Model: "private_media_counts", OwnerUserID: 100, PeerType: domain.PeerTypeUser, PeerID: 100}
	base := &blockingReadModelVersionStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
		hashes:  map[ReadModelKey]int64{key: 11}, // DB is still the stale hash 11 at this moment
	}
	cache := NewCachedReadModelVersionStore(base, time.Hour, 100)

	resultCh := make(chan map[ReadModelKey]int64, 1)
	errCh := make(chan error, 1)
	go func() {
		rows, err := cache.ReadModelHashes(ctx, []ReadModelKey{key})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- rows
	}()

	// Wait until load has claimed the inflight and is blocked inside base.ReadModelHashes.
	<-base.started
	// Simulate NOTIFY delivering an authoritative new hash 99 during load (write and bump epoch).
	cache.UpdateReadModelHash(key, 99)
	// Release load: it returns the stale hash 11; the epoch guard must refuse to overwrite 99 with 11.
	close(base.release)

	select {
	case err := <-errCh:
		t.Fatalf("ReadModelHashes: %v", err)
	case rows := <-resultCh:
		if rows[key] != 99 {
			t.Fatalf("in-flight reader got hash %d, want fresh 99 (stale 11 must not win)", rows[key])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for in-flight reader")
	}

	// Subsequent reads must still be 99 and no longer hit base -- proving the cache was neither polluted by the stale value nor wrongly evicted.
	callsBefore := base.calls.Load()
	rows, err := cache.ReadModelHashes(ctx, []ReadModelKey{key})
	if err != nil {
		t.Fatalf("second ReadModelHashes: %v", err)
	}
	if rows[key] != 99 {
		t.Fatalf("cached hash = %d, want 99 (cache poisoned by stale 11)", rows[key])
	}
	if got := base.calls.Load(); got != callsBefore {
		t.Fatalf("second read hit base (calls %d -> %d): cache was poisoned or evicted", callsBefore, got)
	}
}

func TestCachedReadModelVersionStoreKeyGenerationDoesNotRejectUnrelatedRefill(t *testing.T) {
	ctx := context.Background()
	loading := ReadModelKey{Model: "channel_base", PeerType: domain.PeerTypeChannel, PeerID: 10}
	unrelated := ReadModelKey{Model: "dialog_light", OwnerUserID: 200, PeerType: domain.PeerTypeUser, PeerID: 300}
	base := &blockingReadModelVersionStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
		hashes:  map[ReadModelKey]int64{loading: 11},
	}
	cache := NewCachedReadModelVersionStore(base, time.Hour, 100)

	resultCh := make(chan map[ReadModelKey]int64, 1)
	go func() {
		rows, _ := cache.ReadModelHashes(ctx, []ReadModelKey{loading})
		resultCh <- rows
	}()
	<-base.started
	cache.UpdateReadModelHash(unrelated, 99)
	close(base.release)
	if rows := <-resultCh; rows[loading] != 11 {
		t.Fatalf("loading hash = %d, want 11", rows[loading])
	}
	if _, err := cache.ReadModelHashes(ctx, []ReadModelKey{loading}); err != nil {
		t.Fatal(err)
	}
	if got := base.calls.Load(); got != 1 {
		t.Fatalf("unrelated NOTIFY rejected refill: base calls = %d, want 1", got)
	}
}

func TestCachedReadModelVersionStoreExactUpdateRejectsOnlyChangedBatchKey(t *testing.T) {
	ctx := context.Background()
	a := ReadModelKey{Model: "channel_base", PeerType: domain.PeerTypeChannel, PeerID: 10}
	b := ReadModelKey{Model: "channel_base", PeerType: domain.PeerTypeChannel, PeerID: 11}
	base := &blockingReadModelVersionStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
		hashes: map[ReadModelKey]int64{
			a: 11,
			b: 22,
		},
	}
	cache := NewCachedReadModelVersionStore(base, time.Hour, 100)
	resultCh := make(chan map[ReadModelKey]int64, 1)
	go func() {
		rows, _ := cache.ReadModelHashes(ctx, []ReadModelKey{a, b})
		resultCh <- rows
	}()
	<-base.started
	cache.UpdateReadModelHash(a, 99)
	close(base.release)
	rows := <-resultCh
	if rows[a] != 99 || rows[b] != 22 {
		t.Fatalf("hashes = %+v, want A=99 B=22", rows)
	}
	if _, err := cache.ReadModelHashes(ctx, []ReadModelKey{b}); err != nil {
		t.Fatal(err)
	}
	if got := base.calls.Load(); got != 1 {
		t.Fatalf("exact A update rejected B refill: base calls = %d, want 1", got)
	}
}

type staleThenFreshReadModelVersionStore struct {
	key     ReadModelKey
	first   int64
	second  int64
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *staleThenFreshReadModelVersionStore) ReadModelHash(
	ctx context.Context,
	model string,
	ownerUserID int64,
	peerType domain.PeerType,
	peerID int64,
) (int64, bool, error) {
	key := ReadModelKey{Model: model, OwnerUserID: ownerUserID, PeerType: peerType, PeerID: peerID}
	rows, err := s.ReadModelHashes(ctx, []ReadModelKey{key})
	if err != nil {
		return 0, false, err
	}
	hash := rows[key]
	return hash, hash != 0, nil
}

func (s *staleThenFreshReadModelVersionStore) ReadModelHashes(ctx context.Context, keys []ReadModelKey) (map[ReadModelKey]int64, error) {
	call := s.calls.Add(1)
	if call == 1 {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	hash := s.second
	if call == 1 {
		hash = s.first
	}
	out := make(map[ReadModelKey]int64, len(keys))
	for _, key := range keys {
		if key == s.key {
			out[key] = hash
		}
	}
	return out, nil
}

func TestCachedReadModelVersionStoreInvalidationAndFlushReloadInflight(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*CachedReadModelVersionStore, ReadModelKey)
	}{
		{name: "exact invalidation", mutate: func(cache *CachedReadModelVersionStore, key ReadModelKey) {
			cache.InvalidateReadModel(key)
		}},
		{name: "listener flush", mutate: func(cache *CachedReadModelVersionStore, _ ReadModelKey) {
			cache.FlushReadModelCache()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			key := ReadModelKey{Model: "channel_member", OwnerUserID: 100, PeerType: domain.PeerTypeChannel, PeerID: 10}
			base := &staleThenFreshReadModelVersionStore{
				key: key, first: 11, second: 22,
				started: make(chan struct{}), release: make(chan struct{}),
			}
			cache := NewCachedReadModelVersionStore(base, time.Hour, 100)
			resultCh := make(chan map[ReadModelKey]int64, 1)
			go func() {
				rows, _ := cache.ReadModelHashes(ctx, []ReadModelKey{key})
				resultCh <- rows
			}()
			<-base.started
			test.mutate(cache, key)
			close(base.release)
			if rows := <-resultCh; rows[key] != 22 {
				t.Fatalf("in-flight read returned %d, want reloaded 22", rows[key])
			}
			if got := base.calls.Load(); got != 2 {
				t.Fatalf("base calls = %d, want stale load + reload", got)
			}
			if _, err := cache.ReadModelHashes(ctx, []ReadModelKey{key}); err != nil {
				t.Fatal(err)
			}
			if got := base.calls.Load(); got != 2 {
				t.Fatalf("reloaded value was not cached: calls=%d", got)
			}
		})
	}
}

func TestCachedReadModelVersionStoreUpdateReadModelHashWarmsCache(t *testing.T) {
	ctx := context.Background()
	key := ReadModelKey{Model: "channel_base", PeerType: domain.PeerTypeChannel, PeerID: 10}
	base := &blockingReadModelVersionStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
		hashes:  map[ReadModelKey]int64{key: 11},
	}
	cache := NewCachedReadModelVersionStore(base, time.Hour, 100)

	cache.UpdateReadModelHash(key, 99)
	rows, err := cache.ReadModelHashes(ctx, []ReadModelKey{key})
	if err != nil {
		t.Fatalf("ReadModelHashes: %v", err)
	}
	if rows[key] != 99 {
		t.Fatalf("hash = %d, want notify-provided 99", rows[key])
	}
	if got := base.calls.Load(); got != 0 {
		t.Fatalf("base calls = %d, want cache warmed by notify", got)
	}
}

func TestCachedReadModelVersionStoreEvictsOneLRUEntryWithoutFlush(t *testing.T) {
	ctx := context.Background()
	keys := []ReadModelKey{
		{Model: "channel_base", PeerType: domain.PeerTypeChannel, PeerID: 10},
		{Model: "channel_base", PeerType: domain.PeerTypeChannel, PeerID: 11},
		{Model: "channel_base", PeerType: domain.PeerTypeChannel, PeerID: 12},
	}
	release := make(chan struct{})
	close(release)
	base := &blockingReadModelVersionStore{
		started: make(chan struct{}),
		release: release,
		hashes: map[ReadModelKey]int64{
			keys[0]: 10,
			keys[1]: 11,
			keys[2]: 12,
		},
	}
	cache := NewCachedReadModelVersionStore(base, time.Hour, 2)
	for _, key := range keys[:2] {
		if _, _, err := cache.ReadModelHash(ctx, key.Model, key.OwnerUserID, key.PeerType, key.PeerID); err != nil {
			t.Fatal(err)
		}
	}
	// Refresh key 0 so key 1 becomes the unique LRU victim.
	if _, _, err := cache.ReadModelHash(ctx, keys[0].Model, 0, keys[0].PeerType, keys[0].PeerID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cache.ReadModelHash(ctx, keys[2].Model, 0, keys[2].PeerType, keys[2].PeerID); err != nil {
		t.Fatal(err)
	}
	if got := base.calls.Load(); got != 3 {
		t.Fatalf("base calls after three unique loads = %d, want 3", got)
	}
	if _, _, err := cache.ReadModelHash(ctx, keys[0].Model, 0, keys[0].PeerType, keys[0].PeerID); err != nil {
		t.Fatal(err)
	}
	if got := base.calls.Load(); got != 3 {
		t.Fatalf("recent entry was flushed with capacity eviction: calls=%d", got)
	}
	if _, _, err := cache.ReadModelHash(ctx, keys[1].Model, 0, keys[1].PeerType, keys[1].PeerID); err != nil {
		t.Fatal(err)
	}
	if got := base.calls.Load(); got != 4 {
		t.Fatalf("LRU victim reload calls = %d, want 4", got)
	}
}
