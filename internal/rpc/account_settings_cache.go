package rpc

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

const (
	accountSettingsCacheMaxEntries = 4096
	// accountSettingsCacheTTL 兜底跨实例失效；同实例 Set 即时失效。设置页连续调
	// getGlobalPrivacy/getAccountTTL/getContentSettings/getContactSignUp 时只查一次 PG。
	accountSettingsCacheTTL = 60 * time.Second
)

// accountSettingsCache 缓存 userID→AccountSettings，避免设置页 4 个 get handler 各查
// 一次同一行（N+1）。AccountSettings 全值类型，无需深拷贝。
type accountSettingsCache struct {
	cache *readmodelcache.Cache[int64, domain.AccountSettings]
}

func newAccountSettingsCache(now func() time.Time) *accountSettingsCache {
	return &accountSettingsCache{
		cache: readmodelcache.New[int64, domain.AccountSettings](readmodelcache.Config[int64, domain.AccountSettings]{
			MaxEntries: accountSettingsCacheMaxEntries,
			TTL:        accountSettingsCacheTTL,
			Now:        now,
		}),
	}
}

func (c *accountSettingsCache) getOrLoad(ctx context.Context, userID int64, load func() (domain.AccountSettings, error)) (domain.AccountSettings, error) {
	if c == nil || userID == 0 {
		return load()
	}
	return c.cache.GetOrLoad(ctx, userID, load)
}

func (c *accountSettingsCache) Delete(userID int64) {
	if c == nil || userID == 0 {
		return
	}
	c.cache.Invalidate(userID)
}

func (c *accountSettingsCache) Store(userID int64, settings domain.AccountSettings) {
	if c == nil || userID == 0 {
		return
	}
	c.cache.Store(userID, settings)
}

type accountSettingsBatchReader interface {
	GetAccountSettingsBatch(ctx context.Context, userIDs []int64) (map[int64]domain.AccountSettings, error)
}

func (c *accountSettingsCache) getOrLoadBatch(
	ctx context.Context,
	userIDs []int64,
	svc accountSettingsService,
) (map[int64]domain.AccountSettings, error) {
	if len(userIDs) == 0 {
		return map[int64]domain.AccountSettings{}, nil
	}
	return c.cache.GetOrLoadBatch(
		ctx,
		userIDs,
		func(int64) (int64, bool) { return 0, true },
		func(ctx context.Context, missing []int64) (map[int64]domain.AccountSettings, error) {
			if batch, ok := svc.(accountSettingsBatchReader); ok {
				return batch.GetAccountSettingsBatch(ctx, missing)
			}
			out := make(map[int64]domain.AccountSettings, len(missing))
			for _, userID := range missing {
				settings, err := svc.GetAccountSettings(ctx, userID)
				if err != nil {
					return nil, err
				}
				out[userID] = settings
			}
			return out, nil
		},
	)
}

// cachedAccountSettings 取（缓存的）账号单例设置；服务未接通返回默认。
func (r *Router) cachedAccountSettings(ctx context.Context, userID int64) (domain.AccountSettings, error) {
	svc, ok := r.accountSettingsSvc()
	if !ok {
		return domain.DefaultAccountSettings(), nil
	}
	return r.accountSettings.getOrLoad(ctx, userID, func() (domain.AccountSettings, error) {
		return svc.GetAccountSettings(ctx, userID)
	})
}
