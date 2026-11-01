package bots

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

const (
	botProfileCacheMaxEntries = 100000
	botProfileCacheTTL        = 24 * time.Hour
)

// botProfileValue is the cached value; found=false means "looked up but this bot does not exist"
// (negative caching), preventing an unregistered bot's id from repeatedly hitting the backend.
type botProfileValue struct {
	profile domain.BotProfile
	found   bool
}

// botProfileCache is backed by the unified cache primitive (LRU per-entry eviction / epoch guard / built-in clone).
// Single lookups go through GetOrLoad, batches through GetOrLoadBatch (one LoadEpoch + batched load + per-key epoch write-back).
type botProfileCache struct {
	cache *readmodelcache.Cache[int64, botProfileValue]
}

func newBotProfileCache(max int, ttl time.Duration) *botProfileCache {
	cache := readmodelcache.New[int64, botProfileValue](readmodelcache.Config[int64, botProfileValue]{
		MaxEntries: max,
		TTL:        ttl,
		Clone:      cloneBotProfileValue,
	})
	if cache == nil {
		return nil
	}
	return &botProfileCache{cache: cache}
}

// getOrLoad resolves a single bot; load returns (profile, found, err).
func (c *botProfileCache) getOrLoad(ctx context.Context, botUserID int64, load func() (domain.BotProfile, bool, error)) (domain.BotProfile, bool, error) {
	if c == nil || botUserID == 0 {
		return load()
	}
	v, err := c.cache.GetOrLoad(ctx, botUserID, func() (botProfileValue, error) {
		profile, found, err := load()
		if err != nil {
			return botProfileValue{}, err
		}
		return botProfileValue{profile: normalizeBotProfile(botUserID, profile, found), found: found}, nil
	})
	if err != nil {
		return domain.BotProfile{}, false, err
	}
	return v.profile, v.found, nil
}

// getMany resolves a batch; loadMissing returns the profiles for the misses (existing ones only; a miss is
// treated as a negative result). The returned map contains only existing (found) bots, matching the old getMany semantics.
func (c *botProfileCache) getMany(ctx context.Context, ids []int64, loadMissing func(context.Context, []int64) (map[int64]domain.BotProfile, error)) (map[int64]domain.BotProfile, error) {
	unique := uniqueBotUserIDs(ids)
	if c == nil {
		return loadMissing(ctx, unique)
	}
	values, err := c.cache.GetOrLoadBatch(ctx, unique,
		func(int64) (int64, bool) { return 0, true }, // pure TTL, no version gate
		func(ctx context.Context, missing []int64) (map[int64]botProfileValue, error) {
			loaded, err := loadMissing(ctx, missing)
			if err != nil {
				return nil, err
			}
			out := make(map[int64]botProfileValue, len(missing))
			for _, id := range missing {
				profile, found := loaded[id]
				out[id] = botProfileValue{profile: normalizeBotProfile(id, profile, found), found: found}
			}
			return out, nil
		})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]domain.BotProfile, len(values))
	for id, v := range values {
		if v.found {
			out[id] = v.profile
		}
	}
	return out, nil
}

func (c *botProfileCache) put(botUserID int64, profile domain.BotProfile, found bool) {
	if c == nil || botUserID == 0 {
		return
	}
	c.cache.Store(botUserID, botProfileValue{profile: normalizeBotProfile(botUserID, profile, found), found: found})
}

func (c *botProfileCache) delete(botUserID int64) {
	if c == nil || botUserID == 0 {
		return
	}
	c.cache.Invalidate(botUserID)
}

func (c *botProfileCache) flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}

func normalizeBotProfile(botUserID int64, profile domain.BotProfile, found bool) domain.BotProfile {
	if found && profile.BotUserID == 0 {
		profile.BotUserID = botUserID
	}
	return profile
}

func cloneBotProfileValue(v botProfileValue) botProfileValue {
	v.profile = cloneBotProfile(v.profile)
	return v
}

func cloneBotProfile(profile domain.BotProfile) domain.BotProfile {
	if len(profile.Commands) > 0 {
		profile.Commands = append([]domain.BotCommand(nil), profile.Commands...)
	}
	return profile
}
