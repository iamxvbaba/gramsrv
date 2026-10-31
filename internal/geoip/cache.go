package geoip

import (
	"net/netip"
	"sync"
	"time"
)

// cacheEntry is one cache record. resolved=false is a negative entry: the
// backends were actually queried but produced no usable result (IP not found,
// rate limit, timeout, network error).
type cacheEntry struct {
	location Location
	resolved bool
	expires  time.Time
}

type cacheConfig struct {
	ttl     time.Duration
	negTTL  time.Duration
	maxSize int
}

// cache is a fixed-size IP cache with TTLs that records both hits and misses.
//
// The negative cache is the key to whether this backend is usable: within its
// rate-limit window reallyfreegeoip.org repeatedly returns the same empty result
// for an IP it cannot find, so without caching every session-list open re-pays
// ~0.7s, and those requests extend the 429 window even further -- a negative
// feedback loop that turns occasional rate limiting into the norm.
type cache struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	negTTL  time.Duration
	maxSize int
	entries map[netip.Addr]cacheEntry
}

func newCache(cfg cacheConfig) *cache {
	return &cache{
		now:     time.Now,
		ttl:     positiveDuration(cfg.ttl, defaultCacheTTL),
		negTTL:  positiveDuration(cfg.negTTL, defaultNegativeTTL),
		maxSize: positiveInt(cfg.maxSize, defaultCacheSize),
		entries: make(map[netip.Addr]cacheEntry),
	}
}

// get returns an unexpired entry. The second value means "the cache has this
// record", even if it is a negative entry.
func (c *cache) get(addr netip.Addr) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[addr]
	if !ok {
		return cacheEntry{}, false
	}
	if !c.now().Before(entry.expires) {
		delete(c.entries, addr)
		return cacheEntry{}, false
	}
	return entry, true
}

// put records a successful resolution. Positive entries use a long TTL -- a
// single IP's geographic identity barely changes, and there is no point
// repeatedly re-fetching a result that will not change for freshness' sake.
func (c *cache) put(addr netip.Addr, loc Location) {
	c.set(addr, cacheEntry{location: loc, resolved: true, expires: c.now().Add(c.ttl)})
}

// putMiss records a miss. It uses a short TTL, otherwise one rate limit locks the IP out until the next day.
func (c *cache) putMiss(addr netip.Addr) {
	c.set(addr, cacheEntry{resolved: false, expires: c.now().Add(c.negTTL)})
}

func (c *cache) set(addr netip.Addr, entry cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// When overwriting an existing entry, drop it first if it is expired;
	// otherwise a rapidly rotating set of addresses would keep expired entries
	// packed in the map and prematurely trigger the full clear below.
	if existing, ok := c.entries[addr]; ok && !c.now().Before(existing.expires) {
		delete(c.entries, addr)
	}
	c.entries[addr] = entry
	c.evictLocked()
}

// evictLocked keeps the entry count within maxSize.
//
// It clears expired entries first; if still over the limit it clears everything.
// The cardinality of session IPs grows with online users anyway, and a clear
// costs at most one re-query of the next batch (~0.7s) in exchange for a
// deterministic memory bound that needs no LRU list to maintain -- a worthwhile
// trade for a display-only cache.
func (c *cache) evictLocked() {
	if len(c.entries) <= c.maxSize {
		return
	}
	now := c.now()
	for addr, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, addr)
		}
	}
	if len(c.entries) <= c.maxSize {
		return
	}
	clear(c.entries)
}
