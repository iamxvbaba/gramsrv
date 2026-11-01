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
	defaultMediaCountReadModelTTL = 24 * time.Hour
	mediaCountReadModelMaxEntries = 8192
)

type mediaCountCacheKey struct {
	userID    int64
	channelID int64
}

// mediaCountReadModelCache is backed by the shared cache primitive
// (version gate / epoch guard / LRU / clone).
type mediaCountReadModelCache struct {
	cache *readmodelcache.Cache[mediaCountCacheKey, domain.MediaCategoryCounts]
}

func newMediaCountReadModelCache(ttl time.Duration) *mediaCountReadModelCache {
	if ttl <= 0 {
		ttl = defaultMediaCountReadModelTTL
	}
	return &mediaCountReadModelCache{
		cache: readmodelcache.New[mediaCountCacheKey, domain.MediaCategoryCounts](readmodelcache.Config[mediaCountCacheKey, domain.MediaCategoryCounts]{
			MaxEntries: mediaCountReadModelMaxEntries,
			TTL:        ttl,
			Clone:      cloneMediaCategoryCounts,
		}),
	}
}

func (c *mediaCountReadModelCache) getOrLoad(ctx context.Context, key mediaCountCacheKey, hash int64, load func() (domain.MediaCategoryCounts, error)) (domain.MediaCategoryCounts, error) {
	if c == nil {
		return load()
	}
	return c.cache.GetOrLoadVersioned(ctx, key, hash, load)
}

func (c *mediaCountReadModelCache) invalidateChannel(channelID int64) {
	if c == nil || c.cache == nil || channelID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(key mediaCountCacheKey) bool {
		return key.channelID == channelID
	})
}

func (c *mediaCountReadModelCache) invalidateViewer(userID, channelID int64) {
	if c == nil || c.cache == nil || userID == 0 || channelID == 0 {
		return
	}
	c.cache.Invalidate(mediaCountCacheKey{userID: userID, channelID: channelID})
}

func (c *mediaCountReadModelCache) flush() {
	if c == nil || c.cache == nil {
		return
	}
	c.cache.Flush()
}

func (s *Service) InvalidateChannelMediaCountReadModel(channelID int64) {
	if s == nil || s.mediaCountCache == nil {
		return
	}
	s.mediaCountCache.invalidateChannel(channelID)
}

func (s *Service) InvalidateChannelMediaCountReadModelForViewer(userID, channelID int64) {
	if s == nil || s.mediaCountCache == nil {
		return
	}
	s.mediaCountCache.invalidateViewer(userID, channelID)
}

func (s *Service) FlushChannelMediaCountReadModel() {
	if s == nil || s.mediaCountCache == nil {
		return
	}
	s.mediaCountCache.flush()
}

func (s *Service) cachedChannelMediaCounts(ctx context.Context, userID, channelID int64) (domain.MediaCategoryCounts, error) {
	if s.mediaCountCache == nil || s.versions == nil {
		return s.channels.CountChannelMediaCategories(ctx, userID, channelID)
	}
	hash, err := s.channelMediaCountHash(ctx, userID, channelID)
	if err != nil {
		return nil, err
	}
	if hash == 0 {
		return s.channels.CountChannelMediaCategories(ctx, userID, channelID)
	}
	key := mediaCountCacheKey{userID: userID, channelID: channelID}
	return s.mediaCountCache.getOrLoad(ctx, key, hash, func() (domain.MediaCategoryCounts, error) {
		return s.channels.CountChannelMediaCategories(ctx, userID, channelID)
	})
}

func (s *Service) channelMediaCountHash(ctx context.Context, userID, channelID int64) (int64, error) {
	keys := []store.ReadModelKey{
		{Model: readmodel.ModelChannelMediaCounts, OwnerUserID: 0, PeerType: domain.PeerTypeChannel, PeerID: channelID},
		{Model: readmodel.ModelChannelMember, OwnerUserID: userID, PeerType: domain.PeerTypeChannel, PeerID: channelID},
	}
	rows, err := s.versions.ReadModelHashes(ctx, keys)
	if err != nil {
		return 0, err
	}
	media := rows[keys[0]]
	if media == 0 {
		return 0, nil
	}
	return readmodel.MixHashes(media, rows[keys[1]]), nil
}

func cloneMediaCategoryCounts(in domain.MediaCategoryCounts) domain.MediaCategoryCounts {
	if len(in) == 0 {
		return domain.MediaCategoryCounts{}
	}
	out := make(domain.MediaCategoryCounts, len(in))
	for category, count := range in {
		out[category] = count
	}
	return out
}
