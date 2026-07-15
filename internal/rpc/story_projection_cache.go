package rpc

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamxvbaba/td/tg"
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

// storyProjectionValue 含负缓存:hasRecent=false 表示「查过、该 peer 无 recent 故事」(仍带
// hidden 状态)。tg.RecentStory 按值视为 copy-safe(与原实现一致,不深拷)。
type storyProjectionValue struct {
	recent    tg.RecentStory
	hasRecent bool
	hidden    bool
}

type storyProjectionMaps struct {
	recent map[domain.Peer]tg.RecentStory
	hidden map[domain.Peer]bool
}

// storyProjectionCache 缓存 (viewer,peer)→{recent, hidden},由统一缓存原语承载,批量经
// GetOrLoadBatch(per-peer 查 + 合批 load misses + per-key 写回;纯 TTL,无版本闸门)。
// GetOrLoadBatch 无批量级 singleflight,故此处保留按 (viewer,peer 集) 维度的 sf,防止并发相同
// 投影请求对昂贵的 story 后端形成 thundering herd。
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

// getMany 批量解析 (viewer, peers) 的 recent/hidden 投影。loadMissing 为缺失 peer 计算两张图
// (与 storyProjectionFreshMaps 同形);每个 miss peer 都被缓存(含「无 recent」负结果与 hidden 态)。
// 整体按 (viewer, 排序去重 peer 集) 走 singleflight,并发相同请求只打一次后端。
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
		func(storyProjectionCacheKey) (int64, bool) { return 0, true }, // 纯 TTL,无版本闸门
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
