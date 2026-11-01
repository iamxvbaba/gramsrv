package channels

import (
	"context"
	"time"

	"telesrv/internal/app/readmodel"
	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
	"telesrv/internal/store"
)

const (
	defaultChannelResolveReadModelTTL = 24 * time.Hour
	channelResolveReadModelMaxEntries = 16384
)

// channelResolveReadModelCache is backed by the shared cache primitive
// (version gate / epoch guard / LRU / clone).
type channelResolveReadModelCache struct {
	cache *readmodelcache.Cache[channelViewCacheKey, domain.ChannelView]
}

// authoritativeResolveChannelCache marks a store whose ResolveChannel path is
// already guarded by exact channel/member invalidation and reconnect flushes.
// Wrapping that path in a second version-token cache adds no freshness boundary
// and turns every process-cold access check into a read_model_versions query.
type authoritativeResolveChannelCache interface {
	AuthoritativeResolveChannelCache()
}

func newChannelResolveReadModelCache(ttl time.Duration) *channelResolveReadModelCache {
	if ttl <= 0 {
		ttl = defaultChannelResolveReadModelTTL
	}
	return &channelResolveReadModelCache{
		cache: readmodelcache.New[channelViewCacheKey, domain.ChannelView](readmodelcache.Config[channelViewCacheKey, domain.ChannelView]{
			MaxEntries: channelResolveReadModelMaxEntries,
			TTL:        ttl,
			Clone:      cloneChannelView,
		}),
	}
}

func (c *channelResolveReadModelCache) getOrLoad(ctx context.Context, key channelViewCacheKey, hash int64, load func() (domain.ChannelView, error)) (domain.ChannelView, error) {
	if c == nil {
		return load()
	}
	return c.cache.GetOrLoadVersioned(ctx, key, hash, load)
}

func (s *Service) cachedResolveChannel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error) {
	if _, ok := s.channels.(authoritativeResolveChannelCache); ok {
		return s.channels.ResolveChannel(ctx, userID, channelID)
	}
	if s.resolveCache == nil || s.versions == nil {
		return s.channels.ResolveChannel(ctx, userID, channelID)
	}
	hash, err := s.channelResolveHash(ctx, userID, channelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if hash == 0 {
		return s.channels.ResolveChannel(ctx, userID, channelID)
	}
	key := channelViewCacheKey{userID: userID, channelID: channelID}
	return s.resolveCache.getOrLoad(ctx, key, hash, func() (domain.ChannelView, error) {
		return s.channels.ResolveChannel(ctx, userID, channelID)
	})
}

func (s *Service) channelResolveHash(ctx context.Context, userID, channelID int64) (int64, error) {
	keys := []store.ReadModelKey{
		{Model: readmodel.ModelChannelBase, OwnerUserID: 0, PeerType: domain.PeerTypeChannel, PeerID: channelID},
		{Model: readmodel.ModelChannelMember, OwnerUserID: userID, PeerType: domain.PeerTypeChannel, PeerID: channelID},
	}
	rows, err := s.versions.ReadModelHashes(ctx, keys)
	if err != nil {
		return 0, err
	}
	base := rows[keys[0]]
	if base == 0 {
		return 0, nil
	}
	return readmodel.MixHashes(base, rows[keys[1]]), nil
}
