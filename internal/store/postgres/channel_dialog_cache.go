package postgres

import (
	"context"
	"sync"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

type channelDialogCacheKey struct {
	userID    int64
	channelID int64
}

type channelDialogCacheEntry struct {
	dialog             domain.ChannelDialog
	listVisible        bool
	topMentioned       bool
	topMediaUnread     bool
	topUnreadProjected bool
}

// ChannelDialogCache caches the viewer-scoped channel dialog projection, backed by the common cache
// primitive readmodelcache.Cache (single-entry LRU eviction / epoch guard / built-in singleflight).
//
// Used only on pool (non-transactional) read paths. The cached value depends on three read models:
// channel_base, channel_member(viewer,channel), and dialog_light(viewer,channel); ReadModelChangeListener
// invalidates the corresponding keys on write-side NOTIFY and flushes on reconnect. warm-from-list backfills via put (including DefaultSendAs).
type ChannelDialogCache struct {
	cache *readmodelcache.Cache[channelDialogCacheKey, channelDialogCacheEntry]

	indexMu     sync.Mutex
	channelKeys map[int64]map[channelDialogCacheKey]struct{}
}

func NewChannelDialogCache(max int) *ChannelDialogCache {
	c := &ChannelDialogCache{channelKeys: make(map[int64]map[channelDialogCacheKey]struct{})}
	cache := readmodelcache.New[channelDialogCacheKey, channelDialogCacheEntry](readmodelcache.Config[channelDialogCacheKey, channelDialogCacheEntry]{
		MaxEntries: max,
		Clone:      cloneChannelDialogCacheEntry,
		OnStore:    c.indexEntry,
		OnRemove:   c.unindexEntry,
	})
	if cache == nil {
		return nil
	}
	c.cache = cache
	return c
}

func (c *ChannelDialogCache) get(userID, channelID int64) (domain.ChannelDialog, bool) {
	if c == nil || userID == 0 || channelID == 0 {
		return domain.ChannelDialog{}, false
	}
	entry, ok := c.cache.Peek(channelDialogCacheKey{userID: userID, channelID: channelID})
	return entry.dialog, ok
}

func (c *ChannelDialogCache) getListProjection(userID, channelID int64) (channelDialogCacheEntry, bool) {
	if c == nil || userID == 0 || channelID == 0 {
		return channelDialogCacheEntry{}, false
	}
	entry, ok := c.cache.Peek(channelDialogCacheKey{userID: userID, channelID: channelID})
	return entry, ok && entry.listVisible
}

func (c *ChannelDialogCache) getOrLoad(ctx context.Context, userID, channelID int64, load func() (domain.ChannelDialog, error)) (domain.ChannelDialog, error) {
	if c == nil || userID == 0 || channelID == 0 {
		return load()
	}
	entry, err := c.cache.GetOrLoad(ctx, channelDialogCacheKey{userID: userID, channelID: channelID}, func() (channelDialogCacheEntry, error) {
		dialog, err := load()
		return channelDialogCacheEntry{dialog: dialog}, err
	})
	return entry.dialog, err
}

func (c *ChannelDialogCache) put(dialog domain.ChannelDialog) {
	if c == nil || dialog.UserID == 0 || dialog.ChannelID == 0 {
		return
	}
	c.cache.Store(channelDialogCacheKey{userID: dialog.UserID, channelID: dialog.ChannelID}, channelDialogCacheEntry{dialog: dialog})
}

// cacheEpoch snapshots the epoch before a "list warm write-back"; together with putIfEpoch it closes the
// warm-vs-invalidation race: if an invalidation arrives (epoch increments) between the DB query returning and the write-back, the stale projection write-back is rejected.
func (c *ChannelDialogCache) cacheEpoch() uint64 {
	if c == nil {
		return 0
	}
	return c.cache.LoadEpoch()
}

func (c *ChannelDialogCache) putIfEpoch(dialog domain.ChannelDialog, loadEpoch uint64) {
	if c == nil || dialog.UserID == 0 || dialog.ChannelID == 0 {
		return
	}
	c.cache.StoreIfEpoch(channelDialogCacheKey{userID: dialog.UserID, channelID: dialog.ChannelID}, channelDialogCacheEntry{dialog: dialog}, loadEpoch)
}

func (c *ChannelDialogCache) putListProjectionIfEpoch(
	dialog domain.ChannelDialog,
	topMentioned bool,
	topMediaUnread bool,
	topUnreadProjected bool,
	loadEpoch uint64,
) {
	if c == nil || dialog.UserID == 0 || dialog.ChannelID == 0 {
		return
	}
	c.cache.StoreIfEpoch(
		channelDialogCacheKey{userID: dialog.UserID, channelID: dialog.ChannelID},
		channelDialogCacheEntry{
			dialog:             dialog,
			listVisible:        true,
			topMentioned:       topMentioned,
			topMediaUnread:     topMediaUnread,
			topUnreadProjected: topUnreadProjected,
		},
		loadEpoch,
	)
}

func (c *ChannelDialogCache) delete(userID, channelID int64) {
	if c == nil || userID == 0 || channelID == 0 {
		return
	}
	c.cache.Invalidate(channelDialogCacheKey{userID: userID, channelID: channelID})
}

func (c *ChannelDialogCache) deleteChannel(channelID int64) {
	if c == nil || channelID == 0 {
		return
	}
	c.indexMu.Lock()
	indexed := c.channelKeys[channelID]
	keys := make([]channelDialogCacheKey, 0, len(indexed))
	for key := range indexed {
		keys = append(keys, key)
	}
	c.indexMu.Unlock()
	c.cache.Invalidate(keys...)
}

func (c *ChannelDialogCache) flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
	c.indexMu.Lock()
	c.channelKeys = make(map[int64]map[channelDialogCacheKey]struct{})
	c.indexMu.Unlock()
}

func (c *ChannelDialogCache) indexEntry(key channelDialogCacheKey, _ channelDialogCacheEntry) {
	c.indexMu.Lock()
	keys := c.channelKeys[key.channelID]
	if keys == nil {
		keys = make(map[channelDialogCacheKey]struct{})
		c.channelKeys[key.channelID] = keys
	}
	keys[key] = struct{}{}
	c.indexMu.Unlock()
}

func (c *ChannelDialogCache) unindexEntry(key channelDialogCacheKey, _ channelDialogCacheEntry) {
	c.indexMu.Lock()
	keys := c.channelKeys[key.channelID]
	delete(keys, key)
	if len(keys) == 0 {
		delete(c.channelKeys, key.channelID)
	}
	c.indexMu.Unlock()
}

func cloneChannelDialogCacheEntry(entry channelDialogCacheEntry) channelDialogCacheEntry {
	if entry.dialog.DefaultSendAs != nil {
		peer := *entry.dialog.DefaultSendAs
		entry.dialog.DefaultSendAs = &peer
	}
	return entry
}
