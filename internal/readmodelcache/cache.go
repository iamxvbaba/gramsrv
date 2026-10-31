// Package readmodelcache provides telesrv's unified in-process read-model cache primitive.
//
// It is the single implementation that ~21 hand-rolled read-model caches
// (map + mutex + TTL + whole-table flush + occasional singleflight + occasional
// epoch) converged to. It is **not** a general-purpose response cache: Telegram
// responses are viewer-sensitive, and the cacheable unit remains the per-viewer
// fact/projection defined in docs/read-model-architecture.md. This type is just
// the mechanism -- keys, TTL and version tokens remain each cache's own policy.
//
// For every user it guarantees for free (exactly the class of bugs the
// 2026-06-17 cache audit found):
//   - bounded: LRU evicts single entries, never flushes the whole table
//     (avoiding a thundering-herd reload cliff);
//   - consistent: a load that spans an invalidation will not write a stale
//     value back (epoch guard) -- this once nailed down the version ridge and
//     ChannelMemberCache lost-updates;
//   - deduplicated: concurrent misses on the same key converge into one load
//     (singleflight);
//   - optional version gate: reuse a cache entry only when storedHash == currentHash.
//
// The epoch is deliberately **in-process**: it only closes in-process
// load-vs-invalidate races. Cross-instance freshness is the responsibility of
// the NOTIFY listener (and a durable invalidation layer that has not landed
// yet); this primitive does not change the cross-process contract.
package readmodelcache

import (
	"container/list"
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Config configures a Cache instance. MaxEntries is mandatory: unbounded caches
// cannot be constructed.
type Config[K comparable, V any] struct {
	// MaxEntries is the LRU upper bound. When <=0, New returns nil (equivalent to
	// "caching disabled", following the New*Cache(max<=0)->nil convention used
	// everywhere; all methods are nil-safe and degrade to a direct load).
	MaxEntries int
	// MaxWeight is an optional second capacity bound. When >0 each value's weight
	// is computed by Weight, and the cache satisfies both MaxEntries and MaxWeight;
	// a single item over the limit is still returned this time but is not retained.
	MaxWeight int64
	// Weight computes the relative footprint of a cached value; used only when
	// MaxWeight>0. When nil each item has weight 1.
	Weight func(V) int64
	// TTL is only a safety fallback (out-of-band writes that were missed).
	// 0 = purely event-driven, no time-based expiry.
	TTL time.Duration
	// Clone deep-copies the value at both the store and return boundaries,
	// isolating the caller from aliasing mutations of the cache entry.
	// nil = the value is itself copy-safe (scalar/flat struct) and is not copied.
	Clone func(V) V
	// KeyString produces the singleflight key; when nil it defaults to
	// fmt.Sprint(K). A custom one is needed only when K's fmt representation is
	// ambiguous (two different keys may print the same).
	KeyString func(K) string
	// Now injects a clock, used only for TTL expiry checks; when nil it defaults to
	// time.Now. Production always leaves it empty; tests may inject a fake clock to
	// advance TTL deterministically.
	Now func() time.Time
	// OnStore/OnRemove let dependent inverted indexes stay in sync over the
	// lifecycle. The callbacks run under the cache lock, so they must not call back
	// into this Cache or block; the value received is the immutable clone the cache holds.
	OnStore  func(K, V)
	OnRemove func(K, V)
}

type lruEntry[K comparable, V any] struct {
	key      K
	value    V
	hash     int64
	expireAt time.Time // zero value = no expiry
	weight   int64
}

// Cache is the generic read-model cache. The zero value is unusable and it must
// be constructed via New. A nil *Cache is legal: all methods are nil-safe and
// GetOrLoad degrades to calling load directly.
type Cache[K comparable, V any] struct {
	mu        sync.Mutex
	ll        *list.List // LRU order, Front = most recently used
	items     map[K]*list.Element
	cap       int
	maxWeight int64
	weight    int64
	ttl       time.Duration
	epoch     uint64
	sf        singleflight.Group
	clone     func(V) V
	weigh     func(V) int64
	keyString func(K) string
	now       func() time.Time
	onStore   func(K, V)
	onRemove  func(K, V)

	// batchFlights coordinates individual keys across overlapping concurrent
	// GetOrLoadBatch calls. A singleflight key for the whole input slice cannot
	// coalesce {1,2,3} with {2,3,4}; tracking the misses per key lets the first
	// caller own 2/3 while the second still loads 4 in its own backend batch.
	batchMu      sync.Mutex
	batchFlights map[batchFlightKey[K]]*batchFlight[V]
}

type batchFlightKey[K comparable] struct {
	key       K
	hash      int64
	cacheable bool
	epoch     uint64
}

type batchFlight[V any] struct {
	done  chan struct{}
	value V
	ok    bool
	err   error
	retry bool
}

// New constructs a Cache. When MaxEntries<=0 it returns nil (caching disabled,
// following the existing convention).
func New[K comparable, V any](cfg Config[K, V]) *Cache[K, V] {
	if cfg.MaxEntries <= 0 {
		return nil
	}
	keyString := cfg.KeyString
	if keyString == nil {
		keyString = func(k K) string { return fmt.Sprint(k) }
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Cache[K, V]{
		ll:           list.New(),
		items:        make(map[K]*list.Element, initialMapHint(cfg.MaxEntries)),
		cap:          cfg.MaxEntries,
		maxWeight:    cfg.MaxWeight,
		ttl:          cfg.TTL,
		clone:        cfg.Clone,
		weigh:        cfg.Weight,
		keyString:    keyString,
		now:          now,
		onStore:      cfg.OnStore,
		onRemove:     cfg.OnRemove,
		batchFlights: make(map[batchFlightKey[K]]*batchFlight[V]),
	}
}

func initialMapHint(max int) int {
	if max < 1024 {
		return max
	}
	return 1024
}

type loadResult[V any] struct {
	value  V
	stored bool
}

// GetOrLoad returns on a hit, otherwise calls load through singleflight. The
// load is epoch-guarded: the epoch is snapshotted before the load, and if any
// Invalidate/Flush happened during the load, writing this load's result back to
// the cache is **refused**, and the cache is re-checked/reloaded to get the
// latest value (avoiding a lost-update pinning a stale value into the cache).
func (c *Cache[K, V]) GetOrLoad(ctx context.Context, key K, load func() (V, error)) (V, error) {
	return c.getOrLoad(ctx, key, 0, false, load)
}

// GetOrLoadVersioned adds a version gate on top of GetOrLoad: it reuses a cache
// entry only when storedHash == currentHash, otherwise it reloads.
// currentHash==0 means "version unknown / bypass the version gate" (matching the
// semantics of the existing snap.hash==currentHash check).
func (c *Cache[K, V]) GetOrLoadVersioned(ctx context.Context, key K, currentHash int64, load func() (V, error)) (V, error) {
	return c.getOrLoad(ctx, key, currentHash, true, load)
}

func (c *Cache[K, V]) getOrLoad(ctx context.Context, key K, currentHash int64, versioned bool, load func() (V, error)) (V, error) {
	if c == nil {
		return load()
	}
	for {
		if v, ok := c.lookup(key, currentHash, versioned); ok {
			return v, nil
		}
		res, err, _ := c.sf.Do(c.singleflightKey(key, currentHash, versioned), func() (any, error) {
			// Look up once more after entering singleflight: a concurrent caller may have just written.
			if v, ok := c.lookup(key, currentHash, versioned); ok {
				return loadResult[V]{value: v, stored: true}, nil
			}
			loadEpoch := c.cacheEpoch()
			v, err := load()
			if err != nil {
				return loadResult[V]{}, err
			}
			stored := c.storeIfEpoch(key, v, currentHash, loadEpoch)
			return loadResult[V]{value: c.cloneValue(v), stored: stored}, nil
		})
		if err != nil {
			var zero V
			return zero, err
		}
		result := res.(loadResult[V])
		if result.stored {
			return result.value, nil
		}
		// The store was refused by the epoch guard (an invalidation happened during
		// the load): this load's value may be stale. Re-check the cache -- use a
		// newer value (warm/subsequent load) if present, otherwise reload to get the
		// DB's latest state.
		if err := ctx.Err(); err != nil {
			var zero V
			return zero, err
		}
	}
}

func (c *Cache[K, V]) lookup(key K, currentHash int64, versioned bool) (V, bool) {
	var zero V
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return zero, false
	}
	ent := el.Value.(*lruEntry[K, V])
	if c.expired(ent) {
		// Passive TTL expiry: a pure per-key delete that does **not** bump the
		// epoch -- doing so would wrongly kill an unrelated in-flight load (the
		// epoch is global), causing thrash.
		c.removeElement(el)
		return zero, false
	}
	if versioned && currentHash != 0 && ent.hash != currentHash {
		return zero, false
	}
	c.ll.MoveToFront(el)
	return c.cloneValue(ent.value), true
}

// storeIfEpoch writes only when the epoch is unchanged (no invalidation during
// the load), returning whether it wrote.
func (c *Cache[K, V]) storeIfEpoch(key K, v V, hash int64, loadEpoch uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch != loadEpoch {
		return false
	}
	c.storeLocked(key, v, hash)
	return true
}

func (c *Cache[K, V]) storeLocked(key K, v V, hash int64) {
	weight := c.valueWeight(v)
	if c.maxWeight > 0 && weight > c.maxWeight {
		if el, ok := c.items[key]; ok {
			c.removeElement(el)
		}
		return
	}
	if el, ok := c.items[key]; ok {
		ent := el.Value.(*lruEntry[K, V])
		if c.onRemove != nil {
			c.onRemove(ent.key, ent.value)
		}
		c.weight -= ent.weight
		ent.value = c.cloneValue(v)
		ent.hash = hash
		ent.expireAt = c.expireAt()
		ent.weight = weight
		c.weight += weight
		if c.onStore != nil {
			c.onStore(ent.key, ent.value)
		}
		c.ll.MoveToFront(el)
		c.evictOverflow()
		return
	}
	ent := &lruEntry[K, V]{key: key, value: c.cloneValue(v), hash: hash, expireAt: c.expireAt(), weight: weight}
	c.items[key] = c.ll.PushFront(ent)
	c.weight += weight
	if c.onStore != nil {
		c.onStore(ent.key, ent.value)
	}
	c.evictOverflow()
}

// Store writes an already-in-hand value into the cache (warm-from-list path). It
// does not bump the epoch: it is not an invalidation and should not cancel other
// in-flight loads.
func (c *Cache[K, V]) Store(key K, v V) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.storeLocked(key, v, 0)
	c.mu.Unlock()
}

// LoadEpoch, in "build the value externally then write it back" mode, snapshots
// the epoch before building; the value is then written back with StoreIfEpoch.
// It is for callers whose value is built outside the cache (needs a ctx, multiple
// round trips) and cannot fit into GetOrLoad (such as RPC projections).
func (c *Cache[K, V]) LoadEpoch() uint64 {
	if c == nil {
		return 0
	}
	return c.cacheEpoch()
}

// StoreIfEpoch writes an externally built value back only when the epoch has not
// changed since loadEpoch (no invalidation during the build). Paired with
// LoadEpoch, it opens GetOrLoad's built-in epoch guard to the external-build path.
func (c *Cache[K, V]) StoreIfEpoch(key K, v V, loadEpoch uint64) {
	if c == nil {
		return
	}
	c.storeIfEpoch(key, v, 0, loadEpoch)
}

// StoreVersioned is like Store but carries a version hash (for the warm path of
// version-gated caches).
func (c *Cache[K, V]) StoreVersioned(key K, hash int64, v V) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.storeLocked(key, v, hash)
	c.mu.Unlock()
}

// GetOrLoadBatch resolves several keys in one pass: per-key hits (TTL + optional
// version gate) -> all misses loaded in a single batch -> per-key epoch-guarded
// writes. It serves caches that "look up by key and batch all misses into one
// backend call" (dialog-peer / privacy / photo / bot profile), avoiding a
// single-key GetOrLoad degenerating one batch DB call into N calls.
//
//   - versionOf(key) returns (hash, cacheable): cacheable=false means the key
//     bypasses the cache (always reloaded, never written back, such as a peer
//     with a missing version in dialog-peer); hash!=0 enables the version gate
//     (reuse only when the stored hash matches); hash==0 with cacheable means a
//     pure TTL cache (no version, such as photo/privacy/bot).
//   - loadMissing must return a value for **every** key it receives (including a
//     negative-cache sentinel for "looked up but does not exist"), so negative
//     results are cached too and keys with no result do not repeatedly hit the
//     backend.
//
// If an invalidation arrives during the batch load (epoch change), the whole pass
// is retried, ensuring pre-invalidation batch data does not mask the invalidation
// (stronger than the caches' previous "silently refuse the write but still return
// the old batch data"; ctx cancellation is the fallback against spinning).
func (c *Cache[K, V]) GetOrLoadBatch(
	ctx context.Context,
	keys []K,
	versionOf func(K) (hash int64, cacheable bool),
	loadMissing func(context.Context, []K) (map[K]V, error),
) (map[K]V, error) {
	if len(keys) == 0 {
		return map[K]V{}, nil
	}
	if c == nil {
		return loadMissing(ctx, dedupeKeys(keys))
	}
	for {
		out := make(map[K]V, len(keys))
		loadEpoch := c.cacheEpoch()
		// Snapshot each miss's (hash, cacheable) at lookup time and reuse the same
		// copy on write-back -- never recompute versionOf on write-back: otherwise a
		// key whose version changes between lookup and write would stamp a value
		// loaded under the old hash into the cache with the new hash, and it would
		// then be hit as fresh data (stale-as-fresh, bypassing the version gate).
		missing := make([]batchMiss[K], 0, len(keys))
		seen := make(map[K]struct{}, len(keys))
		for _, key := range keys {
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			hash, cacheable := versionOf(key)
			if cacheable {
				if v, ok := c.lookup(key, hash, hash != 0); ok {
					out[key] = v
					continue
				}
			}
			missing = append(missing, batchMiss[K]{key: key, hash: hash, cacheable: cacheable})
		}
		if len(missing) == 0 {
			return out, nil
		}
		waits, owned := c.claimBatchFlights(missing, loadEpoch)
		if len(owned) > 0 {
			ownedKeys := make([]K, len(owned))
			for i := range owned {
				ownedKeys[i] = owned[i].miss.key
			}
			loaded, loadErr := loadMissing(ctx, ownedKeys)
			retry := false
			if loadErr == nil {
				entries := make([]batchStoreEntry[K, V], 0, len(owned))
				for _, owner := range owned {
					value, ok := loaded[owner.miss.key]
					if ok && owner.miss.cacheable {
						entries = append(entries, batchStoreEntry[K, V]{
							key: owner.miss.key, value: value, hash: owner.miss.hash,
						})
					}
				}
				retry = !c.storeBatchIfEpoch(entries, loadEpoch)
			}
			for _, owner := range owned {
				value, ok := loaded[owner.miss.key]
				c.completeBatchFlight(owner.key, owner.flight, value, ok, loadErr, retry)
			}
		}

		retry := false
		for _, wait := range waits {
			select {
			case <-wait.flight.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if wait.flight.err != nil {
				return nil, wait.flight.err
			}
			if wait.flight.retry {
				retry = true
				continue
			}
			if wait.flight.ok {
				out[wait.miss.key] = c.cloneValue(wait.flight.value)
			}
		}
		if retry {
			// An invalidation arrived during any owner's batch load: all
			// participants re-check, so the pre-invalidation shared flight value
			// does not cross the epoch boundary.
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		return out, nil
	}
}

type batchMiss[K comparable] struct {
	key       K
	hash      int64
	cacheable bool
}

type batchWait[K comparable, V any] struct {
	miss   batchMiss[K]
	key    batchFlightKey[K]
	flight *batchFlight[V]
}

type batchStoreEntry[K comparable, V any] struct {
	key   K
	value V
	hash  int64
}

func (c *Cache[K, V]) claimBatchFlights(
	missing []batchMiss[K],
	epoch uint64,
) (waits []batchWait[K, V], owned []batchWait[K, V]) {
	waits = make([]batchWait[K, V], 0, len(missing))
	owned = make([]batchWait[K, V], 0, len(missing))
	c.batchMu.Lock()
	for _, miss := range missing {
		key := batchFlightKey[K]{key: miss.key, hash: miss.hash, cacheable: miss.cacheable, epoch: epoch}
		flight, found := c.batchFlights[key]
		wait := batchWait[K, V]{miss: miss, key: key, flight: flight}
		if !found {
			flight = &batchFlight[V]{done: make(chan struct{})}
			c.batchFlights[key] = flight
			wait.flight = flight
			owned = append(owned, wait)
		}
		waits = append(waits, wait)
	}
	c.batchMu.Unlock()
	return waits, owned
}

func (c *Cache[K, V]) completeBatchFlight(
	key batchFlightKey[K],
	flight *batchFlight[V],
	value V,
	ok bool,
	err error,
	retry bool,
) {
	c.batchMu.Lock()
	flight.value = c.cloneValue(value)
	flight.ok = ok
	flight.err = err
	flight.retry = retry
	if current := c.batchFlights[key]; current == flight {
		delete(c.batchFlights, key)
	}
	close(flight.done)
	c.batchMu.Unlock()
}

// storeBatchIfEpoch makes the write side of one batch atomic with respect to
// invalidation. Besides avoiding partial warm state, this gives every waiter
// one unambiguous retry decision for the batch generation it joined.
func (c *Cache[K, V]) storeBatchIfEpoch(entries []batchStoreEntry[K, V], expected uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch != expected {
		return false
	}
	for _, entry := range entries {
		c.storeLocked(entry.key, entry.value, entry.hash)
	}
	return true
}

func dedupeKeys[K comparable](keys []K) []K {
	seen := make(map[K]struct{}, len(keys))
	out := make([]K, 0, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

// Peek reads directly without triggering a load and without an LRU touch
// (warm/test path). An expired entry reports a miss but is not deleted (keeping
// Peek side-effect free).
func (c *Cache[K, V]) Peek(key K) (V, bool) {
	var zero V
	if c == nil {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return zero, false
	}
	ent := el.Value.(*lruEntry[K, V])
	if c.expired(ent) {
		return zero, false
	}
	return c.cloneValue(ent.value), true
}

// Invalidate deletes the given keys and bumps the epoch (closing off the
// write-back of any in-flight load).
func (c *Cache[K, V]) Invalidate(keys ...K) {
	if c == nil || len(keys) == 0 {
		return
	}
	c.mu.Lock()
	c.epoch++
	for _, key := range keys {
		if el, ok := c.items[key]; ok {
			c.removeElement(el)
		}
	}
	c.mu.Unlock()
}

// InvalidateWhere deletes all keys satisfying pred and bumps the epoch
// (viewer/channel-dimension fan-out invalidation, replacing the hand-written
// deleteChannel/InvalidateViewer loops in each cache).
func (c *Cache[K, V]) InvalidateWhere(pred func(K) bool) {
	if c == nil || pred == nil {
		return
	}
	c.mu.Lock()
	c.epoch++
	for key, el := range c.items {
		if pred(key) {
			c.removeElement(el)
		}
	}
	c.mu.Unlock()
}

// InvalidateWhereValue is the dependency-aware form of InvalidateWhere. It is
// intended for bounded composite snapshots whose invalidation key is carried
// by the immutable cached value (for example channel_id -> owner dialog page).
// pred runs under the cache lock and therefore must be fast and must not call
// back into this cache.
func (c *Cache[K, V]) InvalidateWhereValue(pred func(K, V) bool) {
	if c == nil || pred == nil {
		return
	}
	c.mu.Lock()
	c.epoch++
	for key, el := range c.items {
		ent := el.Value.(*lruEntry[K, V])
		if pred(key, ent.value) {
			c.removeElement(el)
		}
	}
	c.mu.Unlock()
}

// Flush empties the cache and bumps the epoch (fallback for the listener
// reconnecting after a disconnect).
func (c *Cache[K, V]) Flush() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.epoch++
	if c.onRemove != nil {
		for el := c.ll.Front(); el != nil; el = el.Next() {
			ent := el.Value.(*lruEntry[K, V])
			c.onRemove(ent.key, ent.value)
		}
	}
	c.ll.Init()
	c.items = make(map[K]*list.Element, initialMapHint(c.cap))
	c.weight = 0
	c.mu.Unlock()
}

// Len returns the current number of cache entries (for tests/metrics).
func (c *Cache[K, V]) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	n := c.ll.Len()
	c.mu.Unlock()
	return n
}

// Weight returns the current aggregate configured weight. It is intended for
// bounded observability and tests; callers must not use it as a correctness
// input because Weight is deliberately an approximation chosen by each cache.
func (c *Cache[K, V]) Weight() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	weight := c.weight
	c.mu.Unlock()
	return weight
}

func (c *Cache[K, V]) cacheEpoch() uint64 {
	c.mu.Lock()
	e := c.epoch
	c.mu.Unlock()
	return e
}

func (c *Cache[K, V]) expired(ent *lruEntry[K, V]) bool {
	return c.ttl > 0 && !ent.expireAt.IsZero() && !ent.expireAt.After(c.now())
}

func (c *Cache[K, V]) expireAt() time.Time {
	if c.ttl <= 0 {
		return time.Time{}
	}
	return c.now().Add(c.ttl)
}

func (c *Cache[K, V]) evictOldest() {
	if el := c.ll.Back(); el != nil {
		c.removeElement(el)
	}
}

func (c *Cache[K, V]) evictOverflow() {
	for c.ll.Len() > c.cap || (c.maxWeight > 0 && c.weight > c.maxWeight) {
		if c.ll.Back() == nil {
			return
		}
		c.evictOldest()
	}
}

func (c *Cache[K, V]) removeElement(el *list.Element) {
	ent := el.Value.(*lruEntry[K, V])
	if c.onRemove != nil {
		c.onRemove(ent.key, ent.value)
	}
	c.weight -= ent.weight
	c.ll.Remove(el)
	delete(c.items, ent.key)
}

func (c *Cache[K, V]) valueWeight(v V) int64 {
	if c.maxWeight <= 0 || c.weigh == nil {
		return 1
	}
	weight := c.weigh(v)
	if weight <= 0 {
		return 1
	}
	return weight
}

func (c *Cache[K, V]) cloneValue(v V) V {
	if c.clone == nil {
		return v
	}
	return c.clone(v)
}

func (c *Cache[K, V]) singleflightKey(key K, hash int64, versioned bool) string {
	s := c.keyString(key)
	if versioned && hash != 0 {
		s += "@" + strconv.FormatInt(hash, 10)
	}
	return s
}
