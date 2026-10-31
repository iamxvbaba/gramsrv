package dialogs

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

const (
	defaultDialogListHashCacheTTL = 24 * time.Hour
	dialogListHashCacheMaxEntries = 4096
)

type dialogListHashCacheKey struct {
	userID        int64
	pinnedOnly    bool
	excludePinned bool
	hasFolderID   bool
	folderID      int
	limit         int
}

type dialogListHashValue struct {
	hash  int64
	count int
}

// dialogListHashCache is backed by the shared cache primitive using "build externally then
// write back" (GetDialogs snapshots cacheEpoch before loading → rememberDialogListHash writes
// back via putIfEpoch). The primitive's epoch only increments on Invalidate/Flush (write-driven
// invalidation); a passive TTL expiry touches only the key and does not move the epoch — exactly
// what this cache needs (to avoid wrongly returning NotModified).
type dialogListHashCache struct {
	cache *readmodelcache.Cache[dialogListHashCacheKey, dialogListHashValue]
}

func newDialogListHashCache(ttl time.Duration) *dialogListHashCache {
	if ttl <= 0 {
		ttl = defaultDialogListHashCacheTTL
	}
	return &dialogListHashCache{
		cache: readmodelcache.New[dialogListHashCacheKey, dialogListHashValue](readmodelcache.Config[dialogListHashCacheKey, dialogListHashValue]{
			MaxEntries: dialogListHashCacheMaxEntries,
			TTL:        ttl,
		}),
	}
}

func (s *Service) GetDialogsHash(_ context.Context, userID int64, filter domain.DialogFilter) (domain.DialogHashCheck, error) {
	if s == nil || s.listHashCache == nil || filter.Hash == 0 {
		return domain.DialogHashCheck{}, nil
	}
	key, ok := dialogListHashKey(userID, filter)
	if !ok {
		return domain.DialogHashCheck{}, nil
	}
	snap, ok := s.listHashCache.lookup(key)
	if !ok {
		return domain.DialogHashCheck{}, nil
	}
	return domain.DialogHashCheck{
		Known:   true,
		Matched: snap.hash == filter.Hash,
		Hash:    snap.hash,
		Count:   snap.count,
	}, nil
}

func (s *Service) rememberDialogListHash(userID int64, filter domain.DialogFilter, list domain.DialogList, loadEpoch uint64) {
	if s == nil || s.listHashCache == nil || list.Hash == 0 {
		return
	}
	key, ok := dialogListHashKey(userID, filter)
	if !ok {
		return
	}
	s.listHashCache.putIfEpoch(key, list.Hash, list.Count, loadEpoch)
}

func dialogListHashKey(userID int64, filter domain.DialogFilter) (dialogListHashCacheKey, bool) {
	if userID == 0 || filter.OffsetDate != 0 || filter.OffsetID != 0 || filter.HasOffsetPeer {
		return dialogListHashCacheKey{}, false
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return dialogListHashCacheKey{
		userID:        userID,
		pinnedOnly:    filter.PinnedOnly,
		excludePinned: filter.ExcludePinned,
		hasFolderID:   filter.HasFolderID,
		folderID:      filter.FolderID,
		limit:         limit,
	}, true
}

func (c *dialogListHashCache) lookup(key dialogListHashCacheKey) (dialogListHashValue, bool) {
	if c == nil {
		return dialogListHashValue{}, false
	}
	return c.cache.Peek(key)
}

// putIfEpoch writes only when the epoch is unchanged (no write-driven invalidation during the
// load), closing the stale write-back race.
func (c *dialogListHashCache) putIfEpoch(key dialogListHashCacheKey, hash int64, count int, loadEpoch uint64) {
	if c == nil || key.userID == 0 || hash == 0 {
		return
	}
	c.cache.StoreIfEpoch(key, dialogListHashValue{hash: hash, count: count}, loadEpoch)
}

func (c *dialogListHashCache) cacheEpoch() uint64 {
	if c == nil {
		return 0
	}
	return c.cache.LoadEpoch()
}

func (c *dialogListHashCache) invalidateOwner(userID int64) {
	if c == nil || userID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(key dialogListHashCacheKey) bool { return key.userID == userID })
}

func (c *dialogListHashCache) flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}
