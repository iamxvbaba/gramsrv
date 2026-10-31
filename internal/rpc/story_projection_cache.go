package rpc

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

const (
	storyProjectionCacheTTL        = storyPinnedStoriesCacheTTL
	storyProjectionCacheMaxEntries = 4096
)

type storyProjectionCacheKey struct {
	viewerUserID int64
	peer         domain.Peer
}

// storyProjectionValue includes a negative cache: hasRecent=false means "checked, this
// peer has no recent story" (still carrying the hidden state). tg.RecentStory is
// treated as copy-safe by value (consistent with the original implementation, no deep
// copy).
type storyProjectionValue struct {
	recent    tg.RecentStory
	hasRecent bool
	hidden    bool
}

type storyProjectionMaps struct {
	recent map[domain.Peer]tg.RecentStory
	hidden map[domain.Peer]bool
}

// storyProjectionCache caches (viewer,peer)->{recent, hidden}, carried by the shared
// cache primitive, in batch via GetOrLoadBatch (per-peer lookup + batched load of
// misses + per-key write-back; pure TTL, no version gate). GetOrLoadBatch has no
// batch-level singleflight, so a sf keyed by (viewer, peer set) is kept here to keep
// concurrent identical projection requests from forming a thundering herd against the
// expensive story backend.
type storyProjectionCache struct {
	cache *readmodelcache.Cache[storyProjectionCacheKey, storyProjectionValue]
	sf    singleflight.Group
}

func newStoryProjectionCache(now func() time.Time) *storyProjectionCache {
	return &storyProjectionCache{
		cache: readmodelcache.New[storyProjectionCacheKey, storyProjectionValue](readmodelcache.Config[storyProjectionCacheKey, storyProjectionValue]{
			MaxEntries: storyProjectionCacheMaxEntries,
			TTL:        storyProjectionCacheTTL,
			Now:        now,
		}),
	}
}

// getMany batch-resolves the recent/hidden projections for (viewer, peers).
// loadMissing computes both maps for the missing peers (same shape as
// storyProjectionFreshMaps); every miss peer is cached (including the "no recent"
// negative result and the hidden state). The whole thing goes through singleflight
// keyed by (viewer, sorted deduped peer set), so concurrent identical requests hit
// the backend only once.
func (c *storyProjectionCache) getMany(
	ctx context.Context,
	viewerUserID int64,
	peers []domain.Peer,
	loadMissing func(context.Context, []domain.Peer) (map[domain.Peer]tg.RecentStory, map[domain.Peer]bool),
) (map[domain.Peer]tg.RecentStory, map[domain.Peer]bool) {
	if c == nil {
		return loadMissing(ctx, peers)
	}
	v, _, _ := c.sf.Do(storyProjectionSingleflightKey(viewerUserID, peers), func() (any, error) {
		recent, hidden := c.getManyUncached(ctx, viewerUserID, peers, loadMissing)
		return storyProjectionMaps{recent: recent, hidden: hidden}, nil
	})
	res := v.(storyProjectionMaps)
	return res.recent, res.hidden
}

func (c *storyProjectionCache) getManyUncached(
	ctx context.Context,
	viewerUserID int64,
	peers []domain.Peer,
	loadMissing func(context.Context, []domain.Peer) (map[domain.Peer]tg.RecentStory, map[domain.Peer]bool),
) (map[domain.Peer]tg.RecentStory, map[domain.Peer]bool) {
	keys := make([]storyProjectionCacheKey, 0, len(peers))
	for _, peer := range peers {
		keys = append(keys, storyProjectionCacheKey{viewerUserID: viewerUserID, peer: peer})
	}
	values, _ := c.cache.GetOrLoadBatch(ctx, keys,
		func(storyProjectionCacheKey) (int64, bool) { return 0, true }, // pure TTL, no version gate
		func(ctx context.Context, missing []storyProjectionCacheKey) (map[storyProjectionCacheKey]storyProjectionValue, error) {
			missPeers := make([]domain.Peer, len(missing))
			for i, k := range missing {
				missPeers[i] = k.peer
			}
			recent, hidden := loadMissing(ctx, missPeers)
			out := make(map[storyProjectionCacheKey]storyProjectionValue, len(missing))
			for _, k := range missing {
				v := storyProjectionValue{}
				if story, ok := recent[k.peer]; ok {
					v.recent = story
					v.hasRecent = true
				}
				if state, ok := hidden[k.peer]; ok {
					v.hidden = state
				}
				out[k] = v
			}
			return out, nil
		})
	recentOut := make(map[domain.Peer]tg.RecentStory, len(values))
	hiddenOut := make(map[domain.Peer]bool, len(values))
	for k, v := range values {
		if v.hasRecent {
			recentOut[k.peer] = v.recent
		}
		hiddenOut[k.peer] = v.hidden
	}
	return recentOut, hiddenOut
}

func (c *storyProjectionCache) Delete(viewerUserID int64, peer domain.Peer) {
	if c == nil || viewerUserID == 0 || peer.ID == 0 {
		return
	}
	c.cache.Invalidate(storyProjectionCacheKey{viewerUserID: viewerUserID, peer: peer})
}

func (c *storyProjectionCache) DeleteViewer(viewerUserID int64) {
	if c == nil || viewerUserID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(k storyProjectionCacheKey) bool { return k.viewerUserID == viewerUserID })
}

func (c *storyProjectionCache) DeletePeer(peer domain.Peer) {
	if c == nil || peer.ID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(k storyProjectionCacheKey) bool { return k.peer == peer })
}

func (c *storyProjectionCache) Flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}

func (r *Router) storyProjectionMaps(ctx context.Context, viewerUserID int64, peers []domain.Peer) (map[domain.Peer]tg.RecentStory, map[domain.Peer]bool) {
	if r.deps.Stories == nil || viewerUserID == 0 || len(peers) == 0 {
		return nil, nil
	}
	provider, sparse := r.deps.Stories.(storySparseProjectionProvider)
	if sparse && r.storySparseProjectionCache != nil {
		var (
			activePeers []domain.Peer
			hiddenPeers hiddenStoryPeerSet
		)
		now := int(r.clock.Now().Unix())
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error {
			var err error
			activePeers, err = r.storySparseProjectionCache.activePeers(gctx, provider, peers, now)
			return err
		})
		g.Go(func() error {
			var err error
			hiddenPeers, err = r.storySparseProjectionCache.hiddenPeers(gctx, provider, viewerUserID)
			return err
		})
		if err := g.Wait(); err == nil {
			hidden := make(map[domain.Peer]bool, len(peers))
			for _, peer := range peers {
				_, isHidden := hiddenPeers[peer]
				hidden[peer] = isHidden
			}
			if len(activePeers) == 0 {
				return map[domain.Peer]tg.RecentStory{}, hidden
			}
			if r.storyProjectionCache == nil {
				recent, _ := r.storyProjectionFreshMaps(ctx, viewerUserID, activePeers)
				return recent, hidden
			}
			recent, _ := r.storyProjectionCache.getMany(ctx, viewerUserID, activePeers, func(ctx context.Context, missPeers []domain.Peer) (map[domain.Peer]tg.RecentStory, map[domain.Peer]bool) {
				return r.storyProjectionFreshMaps(ctx, viewerUserID, missPeers)
			})
			return recent, hidden
		} else {
			r.log.Warn("story sparse projection read model failed; using authoritative projection",
				zap.Int64("viewer_user_id", viewerUserID), zap.Int("peer_count", len(peers)), zap.Error(err))
		}
	}
	if r.storyProjectionCache == nil {
		return r.storyProjectionFreshMaps(ctx, viewerUserID, peers)
	}
	return r.storyProjectionCache.getMany(ctx, viewerUserID, peers, func(ctx context.Context, missPeers []domain.Peer) (map[domain.Peer]tg.RecentStory, map[domain.Peer]bool) {
		return r.storyProjectionFreshMaps(ctx, viewerUserID, missPeers)
	})
}

func storyProjectionSingleflightKey(viewerUserID int64, peers []domain.Peer) string {
	uniq := make(map[domain.Peer]struct{}, len(peers))
	keys := make([]domain.Peer, 0, len(peers))
	for _, peer := range peers {
		if peer.ID == 0 {
			continue
		}
		if _, ok := uniq[peer]; ok {
			continue
		}
		uniq[peer] = struct{}{}
		keys = append(keys, peer)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Type != keys[j].Type {
			return keys[i].Type < keys[j].Type
		}
		return keys[i].ID < keys[j].ID
	})
	var b strings.Builder
	b.WriteString(strconv.FormatInt(viewerUserID, 10))
	for _, peer := range keys {
		b.WriteByte('|')
		b.WriteString(string(peer.Type))
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(peer.ID, 10))
	}
	return b.String()
}

func (r *Router) invalidateStoryProjectionCache(viewerUserID int64, peer domain.Peer) {
	if r.storySparseProjectionCache != nil {
		r.storySparseProjectionCache.DeleteViewer(viewerUserID)
	}
	if r.storyProjectionCache != nil {
		r.storyProjectionCache.Delete(viewerUserID, peer)
	}
	if r.storyPinnedCache != nil {
		r.storyPinnedCache.Delete(viewerUserID, peer)
	}
	if r.storyPinnedListCache != nil {
		r.storyPinnedListCache.Delete(viewerUserID, peer)
	}
}

func (r *Router) invalidateStoryProjectionCacheForViewer(viewerUserID int64) {
	if r.storySparseProjectionCache != nil {
		r.storySparseProjectionCache.DeleteViewer(viewerUserID)
	}
	if r.storyProjectionCache != nil {
		r.storyProjectionCache.DeleteViewer(viewerUserID)
	}
	if r.storyPinnedCache != nil {
		r.storyPinnedCache.DeleteViewer(viewerUserID)
	}
	if r.storyPinnedListCache != nil {
		r.storyPinnedListCache.DeleteViewer(viewerUserID)
	}
}

func (r *Router) invalidateStoryProjectionCacheForPeer(peer domain.Peer) {
	if r.storySparseProjectionCache != nil {
		r.storySparseProjectionCache.DeletePeer(peer)
	}
	if r.storyProjectionCache != nil {
		r.storyProjectionCache.DeletePeer(peer)
	}
	if r.storyPinnedCache != nil {
		r.storyPinnedCache.DeletePeer(peer)
	}
	if r.storyPinnedListCache != nil {
		r.storyPinnedListCache.DeletePeer(peer)
	}
}

func (r *Router) InvalidateStoryReadModelViewers(viewerUserIDs ...int64) {
	for _, id := range viewerUserIDs {
		r.invalidateStoryProjectionCacheForViewer(id)
	}
}

func (r *Router) InvalidateStoryReadModelPeer(peer domain.Peer) {
	r.invalidateStoryProjectionCacheForPeer(peer)
}

func (r *Router) FlushStoryReadModelCache() {
	if r.storySparseProjectionCache != nil {
		r.storySparseProjectionCache.Flush()
	}
	if r.storyProjectionCache != nil {
		r.storyProjectionCache.Flush()
	}
	if r.storyPinnedCache != nil {
		r.storyPinnedCache.Flush()
	}
	if r.storyPinnedListCache != nil {
		r.storyPinnedListCache.Flush()
	}
}
