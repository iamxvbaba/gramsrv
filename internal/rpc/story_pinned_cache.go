package rpc

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

// storyPinnedAvailableCache caches (viewer,peer)->has pinned stories, carried by
// the shared cache primitive (LRU single-entry eviction / epoch guard / built-in
// singleflight; pure TTL, no version gate).
type storyPinnedAvailableCache struct {
	cache *readmodelcache.Cache[storyProjectionCacheKey, bool]
}

func newStoryPinnedAvailableCache(now func() time.Time) *storyPinnedAvailableCache {
	return &storyPinnedAvailableCache{
		cache: readmodelcache.New[storyProjectionCacheKey, bool](readmodelcache.Config[storyProjectionCacheKey, bool]{
			MaxEntries: storyProjectionCacheMaxEntries,
			TTL:        storyPinnedStoriesCacheTTL,
			Now:        now,
		}),
	}
}

func (c *storyPinnedAvailableCache) getOrLoad(ctx context.Context, viewerUserID int64, peer domain.Peer, load func() (bool, error)) (bool, error) {
	if c == nil || viewerUserID == 0 || peer.ID == 0 {
		return load()
	}
	return c.cache.GetOrLoad(ctx, storyProjectionCacheKey{viewerUserID: viewerUserID, peer: peer}, load)
}

func (c *storyPinnedAvailableCache) Delete(viewerUserID int64, peer domain.Peer) {
	if c == nil || viewerUserID == 0 || peer.ID == 0 {
		return
	}
	c.cache.Invalidate(storyProjectionCacheKey{viewerUserID: viewerUserID, peer: peer})
}

func (c *storyPinnedAvailableCache) DeleteViewer(viewerUserID int64) {
	if c == nil || viewerUserID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(k storyProjectionCacheKey) bool { return k.viewerUserID == viewerUserID })
}

func (c *storyPinnedAvailableCache) DeletePeer(peer domain.Peer) {
	if c == nil || peer.ID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(k storyProjectionCacheKey) bool { return k.peer == peer })
}

func (c *storyPinnedAvailableCache) Flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}
