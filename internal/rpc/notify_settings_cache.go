package rpc

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

const (
	notifySettingsCacheMaxEntries = 4096
	// notifySettingsCacheTTL is the fallback cross-instance invalidation window; Save/Reset on the
	// same instance invalidates immediately. After a notification-settings change, another
	// instance's dialog-list mute state lags by at most this duration (acceptable).
	notifySettingsCacheTTL = 60 * time.Second
)

// notifySettingsCache caches userID -> the map of that user's whole-peer notification settings,
// eliminating the hot-path cost of querying notify_settings on every getDialogs/getPeerDialogs/
// getFullUser/getFullChannel refresh (a hit costs 0 PG). Built-in TTL + singleflight + LRU.
type notifySettingsCache struct {
	cache *readmodelcache.Cache[int64, map[domain.Peer]domain.PeerNotifySettings]
}

func newNotifySettingsCache(now func() time.Time) *notifySettingsCache {
	return &notifySettingsCache{
		cache: readmodelcache.New[int64, map[domain.Peer]domain.PeerNotifySettings](readmodelcache.Config[int64, map[domain.Peer]domain.PeerNotifySettings]{
			MaxEntries: notifySettingsCacheMaxEntries,
			TTL:        notifySettingsCacheTTL,
			Now:        now,
		}),
	}
}

// getOrLoad returns the cached map (read-only; callers must Clone a single entry before retaining
// it after lookup).
func (c *notifySettingsCache) getOrLoad(ctx context.Context, userID int64, load func() (map[domain.Peer]domain.PeerNotifySettings, error)) (map[domain.Peer]domain.PeerNotifySettings, error) {
	if c == nil || userID == 0 {
		return load()
	}
	return c.cache.GetOrLoad(ctx, userID, load)
}

func (c *notifySettingsCache) Delete(userID int64) {
	if c == nil || userID == 0 {
		return
	}
	c.cache.Invalidate(userID)
}

func (c *notifySettingsCache) Flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}

// userNotifySettings fetches (cached) all whole-peer notification settings for a user; returns nil
// when the service is unavailable.
func (r *Router) userNotifySettings(ctx context.Context, userID int64) map[domain.Peer]domain.PeerNotifySettings {
	svc, ok := r.accountNotifySvc()
	if !ok {
		return nil
	}
	settings, err := r.notifySettings.getOrLoad(ctx, userID, func() (map[domain.Peer]domain.PeerNotifySettings, error) {
		return svc.AllPeerNotifySettings(ctx, userID)
	})
	if err != nil {
		return nil
	}
	return settings
}
