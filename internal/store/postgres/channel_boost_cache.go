package postgres

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

type channelBoostCacheKey struct {
	userID   int64
	peerType domain.PeerType
	peerID   int64
}

type channelBoostPeerTotalCacheKey struct {
	peerType domain.PeerType
	peerID   int64
}

// ChannelBoostCache caches the boost read projection, backed by the common cache primitive
// readmodelcache.Cache (single-entry LRU eviction / epoch guard / built-in singleflight, TTL driven by the primitive's clock).
//
// It is deliberately used only on non-transactional read paths; send permission checks and the boost apply transaction still query
// channel_boost_slots live. selfBoosts caches the viewer-scoped SelfBoostsApplied;
// peerTotals caches the channel's total active boost count for premium.getBoostsStatus.
// A boost's own expiry is filtered by SQL at load time (expires_at > now); this cache's TTL is only a freshness fallback,
// equivalent to the wall-clock now passed by the caller (at runtime now=nowUnix()), so migrating to the primitive's internal clock does not change behavior.
type ChannelBoostCache struct {
	selfBoosts *readmodelcache.Cache[channelBoostCacheKey, int]
	peerTotals *readmodelcache.Cache[channelBoostPeerTotalCacheKey, int]
}

// NewChannelBoostCache creates a boost cache with capacity max and freshness ttl; max<=0 or ttl<=0 returns nil (disabled).
func NewChannelBoostCache(max int, ttl time.Duration) *ChannelBoostCache {
	return newChannelBoostCache(max, ttl, nil)
}

// newChannelBoostCache allows injecting a clock, only for tests to advance the TTL deterministically; now=nil uses the real clock.
func newChannelBoostCache(max int, ttl time.Duration, now func() time.Time) *ChannelBoostCache {
	if ttl <= 0 {
		return nil
	}
	selfBoosts := readmodelcache.New[channelBoostCacheKey, int](readmodelcache.Config[channelBoostCacheKey, int]{
		MaxEntries: max,
		TTL:        ttl,
		Now:        now,
	})
	peerTotals := readmodelcache.New[channelBoostPeerTotalCacheKey, int](readmodelcache.Config[channelBoostPeerTotalCacheKey, int]{
		MaxEntries: max,
		TTL:        ttl,
		Now:        now,
	})
	if selfBoosts == nil || peerTotals == nil {
		return nil
	}
	return &ChannelBoostCache{selfBoosts: selfBoosts, peerTotals: peerTotals}
}

func (c *ChannelBoostCache) get(userID int64, peer domain.Peer) (int, bool) {
	key, ok := newChannelBoostCacheKey(userID, peer)
	if c == nil || !ok {
		return 0, false
	}
	return c.selfBoosts.Peek(key)
}

func (c *ChannelBoostCache) getOrLoad(ctx context.Context, userID int64, peer domain.Peer, load func() (int, error)) (int, error) {
	if c == nil {
		return load()
	}
	key, ok := newChannelBoostCacheKey(userID, peer)
	if !ok {
		return load()
	}
	return c.selfBoosts.GetOrLoad(ctx, key, load)
}

func (c *ChannelBoostCache) put(userID int64, peer domain.Peer, boosts int) {
	key, ok := newChannelBoostCacheKey(userID, peer)
	if c == nil || !ok {
		return
	}
	c.selfBoosts.Store(key, boosts)
}

func (c *ChannelBoostCache) delete(userID int64, peer domain.Peer) {
	key, ok := newChannelBoostCacheKey(userID, peer)
	if c == nil || !ok {
		return
	}
	c.selfBoosts.Invalidate(key)
}

func (c *ChannelBoostCache) deleteUser(userID int64) {
	if c == nil || userID == 0 {
		return
	}
	c.selfBoosts.InvalidateWhere(func(k channelBoostCacheKey) bool { return k.userID == userID })
}

func (c *ChannelBoostCache) getPeerTotal(peer domain.Peer) (int, bool) {
	key, ok := newChannelBoostPeerTotalCacheKey(peer)
	if c == nil || !ok {
		return 0, false
	}
	return c.peerTotals.Peek(key)
}

func (c *ChannelBoostCache) getPeerTotalOrLoad(ctx context.Context, peer domain.Peer, load func() (int, error)) (int, error) {
	if c == nil {
		return load()
	}
	key, ok := newChannelBoostPeerTotalCacheKey(peer)
	if !ok {
		return load()
	}
	return c.peerTotals.GetOrLoad(ctx, key, load)
}

func (c *ChannelBoostCache) putPeerTotal(peer domain.Peer, boosts int) {
	key, ok := newChannelBoostPeerTotalCacheKey(peer)
	if c == nil || !ok {
		return
	}
	c.peerTotals.Store(key, boosts)
}

func (c *ChannelBoostCache) deletePeerTotal(peer domain.Peer) {
	key, ok := newChannelBoostPeerTotalCacheKey(peer)
	if c == nil || !ok {
		return
	}
	c.peerTotals.Invalidate(key)
}

func (c *ChannelBoostCache) flush() {
	if c == nil {
		return
	}
	c.selfBoosts.Flush()
	c.peerTotals.Flush()
}

func newChannelBoostCacheKey(userID int64, peer domain.Peer) (channelBoostCacheKey, bool) {
	if userID == 0 || peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		return channelBoostCacheKey{}, false
	}
	return channelBoostCacheKey{userID: userID, peerType: peer.Type, peerID: peer.ID}, true
}

func newChannelBoostPeerTotalCacheKey(peer domain.Peer) (channelBoostPeerTotalCacheKey, bool) {
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		return channelBoostPeerTotalCacheKey{}, false
	}
	return channelBoostPeerTotalCacheKey{peerType: peer.Type, peerID: peer.ID}, true
}
