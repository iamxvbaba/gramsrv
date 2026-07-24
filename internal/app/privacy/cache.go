package privacy

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
	"telesrv/internal/store"
)

const (
	DefaultPrivacyRulesCacheTTL = 24 * time.Hour

	privacySnapshotMaxOwners = 8192
)

var allPrivacyRuleKeys = []domain.PrivacyKey{
	domain.PrivacyKeyStatusTimestamp,
	domain.PrivacyKeyChatInvite,
	domain.PrivacyKeyPhoneCall,
	domain.PrivacyKeyPhoneP2P,
	domain.PrivacyKeyForwards,
	domain.PrivacyKeyProfilePhoto,
	domain.PrivacyKeyPhoneNumber,
	domain.PrivacyKeyAddedByPhone,
	domain.PrivacyKeyVoiceMessages,
	domain.PrivacyKeyAbout,
	domain.PrivacyKeyBirthday,
	domain.PrivacyKeyStarGiftsAutoSave,
	domain.PrivacyKeyNoPaidMessages,
	domain.PrivacyKeySavedMusic,
}

// privacyRulesMap 是单个 owner 的全部隐私规则(空 map = 查过且无规则,即负缓存)。
type privacyRulesMap map[domain.PrivacyKey]domain.PrivacyRules

// CachedPrivacyStore 是 account privacy rules 的 owner 级 read-model 缓存,由统一缓存原语承载
// (LRU 单条驱逐 / epoch 守卫 / clone)。owner 级、变更稀少:一次性装入某 owner 全部 key,让
// projectBatch/CanSeeMatrix 在内存里判 phone/status/photo 可见性,免去反复规划 account_privacy_rules。
// 单 owner 走 GetOrLoad,多 owner 走 GetOrLoadBatch(一次 LoadEpoch + 合批 ListPrivacyRules + 写回)。
type CachedPrivacyStore struct {
	inner store.PrivacyStore
	cache *readmodelcache.Cache[int64, privacyRulesMap]
}

func NewCachedPrivacyStore(inner store.PrivacyStore, ttl time.Duration) *CachedPrivacyStore {
	if inner == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultPrivacyRulesCacheTTL
	}
	return &CachedPrivacyStore{
		inner: inner,
		cache: readmodelcache.New[int64, privacyRulesMap](readmodelcache.Config[int64, privacyRulesMap]{
			MaxEntries: privacySnapshotMaxOwners,
			TTL:        ttl,
			Clone:      clonePrivacyRulesMap,
		}),
	}
}

func (c *CachedPrivacyStore) GetPrivacyRules(ctx context.Context, ownerUserID int64, key domain.PrivacyKey) (domain.PrivacyRules, bool, error) {
	if ownerUserID == 0 {
		return domain.PrivacyRules{}, false, nil
	}
	rules, err := c.ownerRules(ctx, ownerUserID)
	if err != nil {
		return domain.PrivacyRules{}, false, err
	}
	r, ok := rules[key]
	if !ok {
		return domain.PrivacyRules{}, false, nil
	}
	return cloneRules(r), true, nil
}

func (c *CachedPrivacyStore) SetPrivacyRules(ctx context.Context, rules domain.PrivacyRules) error {
	if err := c.inner.SetPrivacyRules(ctx, rules); err != nil {
		return err
	}
	c.InvalidateOwners(rules.OwnerUserID)
	// 数据写入已提交，预热失败不能伪装成写失败；LISTEN/NOTIFY 也会在每个
	// 实例上再次失效并预热，覆盖本实例通知晚于这里到达的时序。
	_ = c.WarmOwners(ctx, rules.OwnerUserID)
	return nil
}

func (c *CachedPrivacyStore) SetPrivacyRulesWithUpdate(
	ctx context.Context,
	rules domain.PrivacyRules,
	event domain.UpdateEvent,
	excludeAuthKeyID [8]byte,
	excludeSessionID int64,
) (domain.UpdateEvent, error) {
	writer, ok := c.inner.(store.PrivacyUpdateStore)
	if !ok {
		return domain.UpdateEvent{}, domain.ErrPrivacyRuleInvalid
	}
	recorded, err := writer.SetPrivacyRulesWithUpdate(ctx, rules, event, excludeAuthKeyID, excludeSessionID)
	if err != nil {
		return domain.UpdateEvent{}, err
	}
	c.InvalidateOwners(rules.OwnerUserID)
	_ = c.WarmOwners(ctx, rules.OwnerUserID)
	return recorded, nil
}

func (c *CachedPrivacyStore) SupportsDurablePrivacyUpdates() bool {
	if c == nil {
		return false
	}
	capability, ok := c.inner.(interface{ SupportsDurablePrivacyUpdates() bool })
	return ok && capability.SupportsDurablePrivacyUpdates()
}

// WarmOwners 在低频写/变更通知路径一次性装入 owner 的完整规则集。调用方必须先
// InvalidateOwners；epoch 保证预热期间若又发生失效，不会把旧快照写回。
func (c *CachedPrivacyStore) WarmOwners(ctx context.Context, ownerUserIDs ...int64) error {
	owners := dedupPrivacyOwnerIDs(ownerUserIDs)
	if len(owners) == 0 || c == nil || c.cache == nil {
		return nil
	}

	// 隐私规则变更很少、读取极热。必须重建全部 key，
	// 不能只塞本次 key，否则会把 owner 的其它持久规则误当成默认规则。
	loadEpoch := c.cache.LoadEpoch()
	list, err := c.inner.ListPrivacyRules(ctx, owners, allPrivacyRuleKeys)
	if err != nil {
		return err
	}
	snapshots := buildPrivacyRulesByOwner(list, owners)
	for _, ownerUserID := range owners {
		c.cache.StoreIfEpoch(ownerUserID, snapshots[ownerUserID], loadEpoch)
	}
	return nil
}

func (c *CachedPrivacyStore) ListPrivacyRules(ctx context.Context, ownerUserIDs []int64, keys []domain.PrivacyKey) ([]domain.PrivacyRules, error) {
	owners := dedupPrivacyOwnerIDs(ownerUserIDs)
	if len(owners) == 0 || len(keys) == 0 {
		return nil, nil
	}
	byOwner, err := c.ownerRulesBatch(ctx, owners)
	if err != nil {
		return nil, err
	}
	keySet := make(map[domain.PrivacyKey]struct{}, len(keys))
	for _, key := range keys {
		keySet[key] = struct{}{}
	}
	out := make([]domain.PrivacyRules, 0, len(owners)*len(keys))
	for _, owner := range owners {
		for key, rules := range byOwner[owner] {
			if _, want := keySet[key]; !want {
				continue
			}
			out = append(out, cloneRules(rules))
		}
	}
	return out, nil
}

func (c *CachedPrivacyStore) ownerRules(ctx context.Context, ownerUserID int64) (privacyRulesMap, error) {
	load := func() (privacyRulesMap, error) {
		list, err := c.inner.ListPrivacyRules(ctx, []int64{ownerUserID}, allPrivacyRuleKeys)
		if err != nil {
			return nil, err
		}
		return buildPrivacyRulesByOwner(list, []int64{ownerUserID})[ownerUserID], nil
	}
	if c == nil || c.cache == nil {
		return load()
	}
	return c.cache.GetOrLoad(ctx, ownerUserID, load)
}

func (c *CachedPrivacyStore) ownerRulesBatch(ctx context.Context, owners []int64) (map[int64]privacyRulesMap, error) {
	loadMissing := func(ctx context.Context, missing []int64) (map[int64]privacyRulesMap, error) {
		list, err := c.inner.ListPrivacyRules(ctx, missing, allPrivacyRuleKeys)
		if err != nil {
			return nil, err
		}
		return buildPrivacyRulesByOwner(list, missing), nil
	}
	if c == nil || c.cache == nil {
		return loadMissing(ctx, owners)
	}
	return c.cache.GetOrLoadBatch(ctx, owners,
		func(int64) (int64, bool) { return 0, true }, // 纯 TTL,无版本闸门
		loadMissing,
	)
}

func (c *CachedPrivacyStore) InvalidateOwners(ids ...int64) {
	if c == nil || c.cache == nil || len(ids) == 0 {
		return
	}
	nonZero := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id != 0 {
			nonZero = append(nonZero, id)
		}
	}
	c.cache.Invalidate(nonZero...)
}

func (c *CachedPrivacyStore) FlushReadModelCache() {
	if c == nil || c.cache == nil {
		return
	}
	c.cache.Flush()
}

// InvalidateOwners lets Service be registered as the single privacy read-model
// cache group: rule snapshots and relationship facts then share one invalidation
// lifecycle.
func (s *Service) InvalidateOwners(ids ...int64) {
	if s == nil || s.rules == nil {
		return
	}
	if cache, ok := s.rules.(interface{ InvalidateOwners(...int64) }); ok {
		cache.InvalidateOwners(ids...)
	}
}

func (s *Service) WarmOwners(ctx context.Context, ids ...int64) error {
	if s == nil || s.rules == nil {
		return nil
	}
	if cache, ok := s.rules.(interface {
		WarmOwners(context.Context, ...int64) error
	}); ok {
		return cache.WarmOwners(ctx, ids...)
	}
	return nil
}

func (s *Service) FlushReadModelCache() {
	if s == nil {
		return
	}
	if cache, ok := s.rules.(interface{ FlushReadModelCache() }); ok {
		cache.FlushReadModelCache()
	}
	s.flushFactCaches()
}

// buildPrivacyRulesByOwner 把扁平规则按 owner 归组;每个 owner 都建一个条目(无规则即空 map),
// 这样「查过且无规则」的 owner 也被负缓存,不会反复打后端。
func buildPrivacyRulesByOwner(list []domain.PrivacyRules, owners []int64) map[int64]privacyRulesMap {
	out := make(map[int64]privacyRulesMap, len(owners))
	for _, owner := range owners {
		out[owner] = make(privacyRulesMap)
	}
	for _, item := range list {
		if item.OwnerUserID == 0 || item.Key == "" {
			continue
		}
		m, ok := out[item.OwnerUserID]
		if !ok {
			m = make(privacyRulesMap)
			out[item.OwnerUserID] = m
		}
		m[item.Key] = cloneRules(item)
	}
	return out
}

func clonePrivacyRulesMap(in privacyRulesMap) privacyRulesMap {
	if in == nil {
		return nil
	}
	out := make(privacyRulesMap, len(in))
	for key, rules := range in {
		out[key] = cloneRules(rules)
	}
	return out
}

func dedupPrivacyOwnerIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
