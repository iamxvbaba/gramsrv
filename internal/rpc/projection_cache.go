package rpc

import (
	"time"

	"telesrv/internal/readmodelcache"
)

// projectionCache is the shared core of the RPC layer's per-viewer TTL projection
// caches, built on readmodelcache.Cache. Four structurally identical projection
// caches (userFull/peerSettings/channelFull/channelFullBot) converge onto it,
// differing only in key type, value type, clone and invalidation dimension; TTL
// and clock, LRU single-entry eviction and the epoch guard are all carried by the
// primitive.
//
// These projections use the "build externally, then write back" pattern
// (LoadEpoch -> build inside the handler -> StoreIfEpoch) rather than GetOrLoad:
// projections need ctx, multiple round-trips and enrichment, and cannot be packed
// into a single load callback. The epoch guard ensures an invalidation that arrives
// during a build is not overwritten by a pre-invalidation snapshot (avoiding a
// stale per-viewer projection lingering for a full TTL).
type projectionCache[K comparable, V any] struct {
	cache *readmodelcache.Cache[K, V]
}

func newProjectionCache[K comparable, V any](max int, ttl time.Duration, clock func() time.Time, clone func(V) V) *projectionCache[K, V] {
	return &projectionCache[K, V]{
		cache: readmodelcache.New[K, V](readmodelcache.Config[K, V]{
			MaxEntries: max,
			TTL:        ttl,
			Now:        clock,
			Clone:      clone,
		}),
	}
}

// lookup returns the cache entry (TTL expiry counts as a miss); clone is done by
// the primitive at the return boundary.
func (c *projectionCache[K, V]) lookup(key K) (V, bool) {
	if c == nil {
		var zero V
		return zero, false
	}
	return c.cache.Peek(key)
}

// storeIfEpoch writes back an externally built value only if the epoch has not
// changed since loadEpoch.
func (c *projectionCache[K, V]) storeIfEpoch(key K, v V, loadEpoch uint64) {
	if c == nil {
		return
	}
	c.cache.StoreIfEpoch(key, v, loadEpoch)
}

// LoadEpoch snapshots the epoch before building; paired with storeIfEpoch it
// implements the epoch guard for the external-build path.
func (c *projectionCache[K, V]) LoadEpoch() uint64 {
	if c == nil {
		return 0
	}
	return c.cache.LoadEpoch()
}

func (c *projectionCache[K, V]) deleteKey(key K) {
	if c == nil {
		return
	}
	c.cache.Invalidate(key)
}

func (c *projectionCache[K, V]) deleteWhere(pred func(K) bool) {
	if c == nil {
		return
	}
	c.cache.InvalidateWhere(pred)
}

// Clear empties the cache and bumps the epoch (fallback for a listener reconnect).
func (c *projectionCache[K, V]) Clear() {
	if c == nil {
		return
	}
	c.cache.Flush()
}
