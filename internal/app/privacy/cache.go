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

// privacyRulesMap holds all privacy rules of a single owner (an empty map = looked up with no rules, i.e. a negative cache).
type privacyRulesMap map[domain.PrivacyKey]domain.PrivacyRules

// CachedPrivacyStore is an owner-level read-model cache of account privacy rules carried by
// the shared cache primitive (single-entry LRU eviction / epoch guard / clone). Owner-level
// with rare changes: it loads all keys of an owner at once so projectBatch/CanSeeMatrix can
// decide phone/status/photo visibility in memory, avoiding repeatedly querying
// account_privacy_rules. A single owner goes through GetOrLoad; multiple owners go through
// GetOrLoadBatch (one LoadEpoch + batched ListPrivacyRules + write-back).
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
	// The data write is already committed; a failed warm-up must not masquerade as a write
	// failure. LISTEN/NOTIFY also invalidates and warms up again on every instance, covering
	// the timing where a notification reaches this instance later than this point.
	_ = c.WarmOwners(ctx, rules.OwnerUserID)
	return nil
}

// WarmOwners loads an owner's complete rule set in one go on the low-frequency
// write/change-notification path. The caller must InvalidateOwners first; the epoch ensures
// that if another invalidation happens during warm-up, the old snapshot is not written back.
func (c *CachedPrivacyStore) WarmOwners(ctx context.Context, ownerUserIDs ...int64) error {
	owners := dedupPrivacyOwnerIDs(ownerUserIDs)
	if len(owners) == 0 || c == nil || c.cache == nil {
		return nil
	}

	// Privacy rules change rarely and are read very frequently. All keys must be rebuilt; we
	// must not stuff in only this call's keys, otherwise the owner's other persistent rules
	// would be mistaken for default rules.
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
		func(int64) (int64, bool) { return 0, true }, // pure TTL, no version gate
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

// buildPrivacyRulesByOwner groups flat rules by owner; an entry is created for every owner
// (an empty map when there are no rules), so an owner that was "looked up with no rules" is
// also negatively cached and does not repeatedly hit the backend.
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
