package postgres

import (
	"context"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

type channelMemberCacheKey struct {
	channelID int64
	userID    int64
}

// ChannelMemberCache is the in-process read-model cache of a channel_members row,
// backed by the common cache primitive readmodelcache.Cache (single-entry LRU eviction / epoch guard / built-in singleflight).
//
// It is used only on pool read paths, bypassed entirely inside transactions; consistency is driven by the read_model trigger + LISTEN/NOTIFY
// invalidation. channel_members carries both member access and read boundary state, so a listener
// reconnect flushes the whole table, avoiding stale permission/visibility reads when a notification is lost. The epoch guard ensures that invalidating a kicked member
// is not overwritten by a stale Active row from an in-flight load—otherwise a kicked member could keep reading history, an access-control bypass.
type ChannelMemberCache struct {
	cache *readmodelcache.Cache[channelMemberCacheKey, domain.ChannelMember]
}

// NewChannelMemberCache creates a member cache with capacity max; max<=0 returns nil (disabled; callers skip on nil).
// domain.ChannelMember is a flat value (no shared mutable fields), so no Clone is set.
func NewChannelMemberCache(max int) *ChannelMemberCache {
	cache := readmodelcache.New[channelMemberCacheKey, domain.ChannelMember](readmodelcache.Config[channelMemberCacheKey, domain.ChannelMember]{
		MaxEntries: max,
	})
	if cache == nil {
		return nil
	}
	return &ChannelMemberCache{cache: cache}
}

func (c *ChannelMemberCache) get(channelID, userID int64) (domain.ChannelMember, bool) {
	if c == nil || channelID == 0 || userID == 0 {
		return domain.ChannelMember{}, false
	}
	return c.cache.Peek(channelMemberCacheKey{channelID: channelID, userID: userID})
}

func (c *ChannelMemberCache) getOrLoad(ctx context.Context, channelID, userID int64, load func() (domain.ChannelMember, error)) (domain.ChannelMember, error) {
	if c == nil {
		return load()
	}
	return c.cache.GetOrLoad(ctx, channelMemberCacheKey{channelID: channelID, userID: userID}, load)
}

func (c *ChannelMemberCache) put(member domain.ChannelMember) {
	if c == nil || member.ChannelID == 0 || member.UserID == 0 {
		return
	}
	c.cache.Store(channelMemberCacheKey{channelID: member.ChannelID, userID: member.UserID}, member)
}

func (c *ChannelMemberCache) cacheEpoch() uint64 {
	if c == nil {
		return 0
	}
	return c.cache.LoadEpoch()
}

// putIfEpoch prevents a materialized owner snapshot that raced a membership
// invalidation from restoring stale access rights after the listener advanced
// the cache epoch.
func (c *ChannelMemberCache) putIfEpoch(member domain.ChannelMember, loadEpoch uint64) {
	if c == nil || member.ChannelID == 0 || member.UserID == 0 {
		return
	}
	c.cache.StoreIfEpoch(
		channelMemberCacheKey{channelID: member.ChannelID, userID: member.UserID},
		member,
		loadEpoch,
	)
}

func (c *ChannelMemberCache) delete(channelID, userID int64) {
	if c == nil || channelID == 0 || userID == 0 {
		return
	}
	c.cache.Invalidate(channelMemberCacheKey{channelID: channelID, userID: userID})
}

func (c *ChannelMemberCache) deleteChannel(channelID int64) {
	if c == nil || channelID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(k channelMemberCacheKey) bool { return k.channelID == channelID })
}

func (c *ChannelMemberCache) flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}
