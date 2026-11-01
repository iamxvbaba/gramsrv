package channels

import (
	"context"
	"errors"
	"fmt"
	"time"

	"telesrv/internal/app/readmodel"
	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
	"telesrv/internal/store"
)

const (
	defaultActiveChannelIDsReadModelTTL = 24 * time.Hour
	activeChannelIDsReadModelMaxEntries = 32768
	activeChannelIDsNoVersionHash       = -1
	activeChannelIDsStableCutAttempts   = 2
)

var errActiveChannelIDsGenerationChanged = errors.New("active channel IDs generation changed")

// ActiveChannelIDsReadModelMetrics records bounded shared-cache outcomes.
// User IDs and page selectors are deliberately excluded.
type ActiveChannelIDsReadModelMetrics interface {
	ActiveChannelIDsCache(outcome string)
}

type activeChannelIDsCacheKey struct {
	userID         int64
	afterChannelID int64
	limit          int
}

// activeChannelIDsReadModelCache is backed by the shared cache primitive
// (version gate / epoch guard / LRU / clone).
// When no version is present the activeChannelIDsNoVersionHash sentinel is used as
// the version; TTL+epoch invalidation still applies.
type activeChannelIDsReadModelCache struct {
	cache *readmodelcache.Cache[activeChannelIDsCacheKey, []int64]
}

func newActiveChannelIDsReadModelCache(maxEntries int, ttl time.Duration) *activeChannelIDsReadModelCache {
	if maxEntries <= 0 {
		maxEntries = activeChannelIDsReadModelMaxEntries
	}
	if ttl <= 0 {
		ttl = defaultActiveChannelIDsReadModelTTL
	}
	return &activeChannelIDsReadModelCache{
		cache: readmodelcache.New[activeChannelIDsCacheKey, []int64](readmodelcache.Config[activeChannelIDsCacheKey, []int64]{
			MaxEntries: maxEntries,
			TTL:        ttl,
			Clone:      cloneInt64s,
		}),
	}
}

func (c *activeChannelIDsReadModelCache) getOrLoad(ctx context.Context, key activeChannelIDsCacheKey, hash int64, load func() ([]int64, error)) ([]int64, error) {
	if c == nil {
		return load()
	}
	return c.cache.GetOrLoadVersioned(ctx, key, hash, load)
}

func (c *activeChannelIDsReadModelCache) invalidateUsers(userIDs ...int64) {
	if c == nil || len(userIDs) == 0 {
		return
	}
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID != 0 {
			seen[userID] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return
	}
	c.cache.InvalidateWhere(func(k activeChannelIDsCacheKey) bool {
		_, ok := seen[k.userID]
		return ok
	})
}

func (s *Service) cachedActiveChannelIDsForUser(ctx context.Context, userID, afterChannelID int64, limit int) ([]int64, error) {
	if s.activeIDsShared == nil {
		if s.activeIDsCache == nil || s.versions == nil {
			return s.channels.ListActiveChannelIDsForUser(ctx, userID, afterChannelID, limit)
		}
		hash, err := s.activeChannelIDsGeneration(ctx, userID)
		if err != nil {
			return nil, err
		}
		key := activeChannelIDsCacheKey{userID: userID, afterChannelID: afterChannelID, limit: limit}
		return s.activeIDsCache.getOrLoad(ctx, key, hash, func() ([]int64, error) {
			return s.channels.ListActiveChannelIDsForUser(ctx, userID, afterChannelID, limit)
		})
	}
	if s.activeIDsCache == nil || s.versions == nil || s.activeIDsLoader == nil {
		return nil, errors.New("shared active channel IDs read model is incompletely configured")
	}
	key := activeChannelIDsCacheKey{userID: userID, afterChannelID: afterChannelID, limit: limit}
	for attempt := 0; attempt < activeChannelIDsStableCutAttempts; attempt++ {
		generation, err := s.activeChannelIDsGeneration(ctx, userID)
		if err != nil {
			return nil, err
		}
		channelIDs, err := s.activeIDsCache.getOrLoad(ctx, key, generation, func() ([]int64, error) {
			return s.loadSharedActiveChannelIDsPage(ctx, key, generation)
		})
		if errors.Is(err, errActiveChannelIDsGenerationChanged) {
			s.recordActiveChannelIDsCache("generation_retry")
			continue
		}
		if err != nil {
			return nil, err
		}
		currentGeneration, err := s.activeChannelIDsGeneration(ctx, userID)
		if err != nil {
			return nil, err
		}
		if currentGeneration != generation {
			s.recordActiveChannelIDsCache("generation_retry")
			s.activeIDsCache.invalidateUsers(userID)
			continue
		}
		s.recordActiveChannelIDsCache("served")
		return channelIDs, nil
	}
	return nil, errActiveChannelIDsGenerationChanged
}

func (s *Service) activeChannelIDsGeneration(ctx context.Context, userID int64) (int64, error) {
	if s == nil || s.versions == nil {
		return 0, errors.New("active channel IDs read model requires durable versions")
	}
	hash, ok, err := s.versions.ReadModelHash(ctx, readmodel.ModelChannelActiveIDs, userID, domain.PeerTypeUser, userID)
	if err != nil {
		return 0, err
	}
	if !ok || hash == 0 {
		return activeChannelIDsNoVersionHash, nil
	}
	return hash, nil
}

func (s *Service) loadSharedActiveChannelIDsPage(
	ctx context.Context,
	key activeChannelIDsCacheKey,
	generation int64,
) ([]int64, error) {
	sharedKey := store.ActiveChannelIDsPageKey{
		UserID: key.userID, Generation: generation,
		AfterChannelID: key.afterChannelID, Limit: key.limit,
	}
	channelIDs, found, err := s.activeIDsShared.GetActiveChannelIDsPage(ctx, sharedKey)
	if err != nil {
		s.recordActiveChannelIDsCache("read_error")
		return nil, err
	}
	if found {
		s.recordActiveChannelIDsCache("hit")
		return channelIDs, nil
	}
	s.recordActiveChannelIDsCache("miss")
	channelIDs, err = s.activeIDsLoader.ListActiveChannelIDsForUser(
		ctx, key.userID, key.afterChannelID, key.limit,
	)
	if err != nil {
		return nil, err
	}
	if generation == activeChannelIDsNoVersionHash && len(channelIDs) != 0 {
		return nil, fmt.Errorf("active channel IDs generation missing for non-empty owner %d", key.userID)
	}
	currentGeneration, err := s.activeChannelIDsGeneration(ctx, key.userID)
	if err != nil {
		return nil, err
	}
	if currentGeneration != generation {
		return nil, errActiveChannelIDsGenerationChanged
	}
	if err := s.activeIDsShared.PutActiveChannelIDsPage(ctx, sharedKey, channelIDs); err != nil {
		s.recordActiveChannelIDsCache("write_error")
		return nil, err
	}
	s.recordActiveChannelIDsCache("fill")
	return channelIDs, nil
}

func (s *Service) recordActiveChannelIDsCache(outcome string) {
	if s != nil && s.activeIDsMetrics != nil {
		s.activeIDsMetrics.ActiveChannelIDsCache(outcome)
	}
}

func cloneInt64s(in []int64) []int64 {
	if len(in) == 0 {
		return nil
	}
	return append([]int64(nil), in...)
}
