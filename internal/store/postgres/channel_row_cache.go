package postgres

import (
	"context"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

// ChannelRowCache is a strongly consistent in-process cache of the "shared channel row"
// (one channels-table row → domain.Channel), backed by the unified cache primitive
// readmodelcache.Cache (per-entry LRU eviction / epoch guard / built-in singleflight).
//
// It caches only the viewer-agnostic, infrequently changing channel row; the per-viewer member
// (permissions/read state) is isolated separately by ChannelMemberCache keyed by
// (channel_id,user_id), and dialog (top/unread) and boost are still queried live from PG.
//
// Consistency relies on real-time invalidation through the unified read-model PG LISTEN/NOTIFY
// (see ReadModelChangeListener) rather than a TTL: the read-model trigger on the channels table
// publishes a channel_base event on commit, and the listener deletes the corresponding entry on
// receipt; on (re)connect the listener flushes the whole table, covering notifications lost in
// the disconnect window. There is no TTL, so the epoch guard is the only safeguard against
// permanent staleness (built into the primitive).
//
// It is consumed only on the pool handle (not in a transaction) (see ChannelStore.cacheActive):
// inside a transaction everything is read live to guarantee read-your-write.
type ChannelRowCache struct {
	cache *readmodelcache.Cache[int64, domain.Channel]
}

// NewChannelRowCache creates a channel row cache with capacity max; max<=0 returns nil
// (meaning disabled, and callers skip on nil).
func NewChannelRowCache(max int) *ChannelRowCache {
	cache := readmodelcache.New[int64, domain.Channel](readmodelcache.Config[int64, domain.Channel]{
		MaxEntries: max,
		Clone:      cloneChannelRow,
	})
	if cache == nil {
		return nil
	}
	return &ChannelRowCache{cache: cache}
}

func (c *ChannelRowCache) get(id int64) (domain.Channel, bool) {
	if c == nil || id == 0 {
		return domain.Channel{}, false
	}
	return c.cache.Peek(id)
}

func (c *ChannelRowCache) getOrLoad(ctx context.Context, id int64, load func() (domain.Channel, error)) (domain.Channel, error) {
	if c == nil {
		return load()
	}
	return c.cache.GetOrLoad(ctx, id, load)
}

// getOrLoadBatch resolves a page of shared channel rows with one database read
// for all misses. A zero-ID Channel is the negative-cache sentinel for an ID
// that disappeared between the owner-state query and shared-row hydration;
// channel_base invalidation removes both positive and negative entries.
func (c *ChannelRowCache) getOrLoadBatch(
	ctx context.Context,
	ids []int64,
	load func(context.Context, []int64) (map[int64]domain.Channel, error),
) (map[int64]domain.Channel, error) {
	if c == nil {
		return load(ctx, ids)
	}
	return c.cache.GetOrLoadBatch(
		ctx,
		ids,
		func(int64) (int64, bool) { return 0, true },
		func(ctx context.Context, missing []int64) (map[int64]domain.Channel, error) {
			loaded, err := load(ctx, missing)
			if err != nil {
				return nil, err
			}
			// GetOrLoadBatch requires an explicit value for every key so absent
			// rows do not immediately stampede the database again.
			for _, id := range missing {
				if _, ok := loaded[id]; !ok {
					loaded[id] = domain.Channel{}
				}
			}
			return loaded, nil
		},
	)
}

func (c *ChannelRowCache) put(ch domain.Channel) {
	if c == nil || ch.ID == 0 {
		return
	}
	c.cache.Store(ch.ID, ch)
}

// delete invalidates a single channel (called when the listener receives a change notification for that id).
func (c *ChannelRowCache) delete(id int64) {
	if c == nil || id == 0 {
		return
	}
	c.cache.Invalidate(id)
}

// flush clears the whole table (called by the listener on every (re)connect after establishing
// LISTEN, covering notifications lost in the disconnect window).
func (c *ChannelRowCache) flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}

// cloneChannelRow deep-copies the channel row's mutable fields (slices), so the cache and the
// caller do not alias-pollute each other.
func cloneChannelRow(ch domain.Channel) domain.Channel {
	if ch.PhotoStripped != nil {
		ch.PhotoStripped = append([]byte(nil), ch.PhotoStripped...)
	}
	if ch.ReactionPolicy.Emoticons != nil {
		ch.ReactionPolicy.Emoticons = append([]string(nil), ch.ReactionPolicy.Emoticons...)
	}
	if ch.ReactionPolicy.CustomEmojiIDs != nil {
		ch.ReactionPolicy.CustomEmojiIDs = append([]int64(nil), ch.ReactionPolicy.CustomEmojiIDs...)
	}
	ch.Wallpaper = domain.CloneWallpaperPtr(ch.Wallpaper)
	return ch
}
