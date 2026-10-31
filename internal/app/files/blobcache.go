package files

import (
	"container/list"
	"strconv"
	"sync"
	"time"

	"telesrv/internal/domain"
)

// stickerSetNegativeCache is the short-TTL negative cache of "sticker sets not found by ref".
// A client repeatedly calls getStickerSet for an unseeded short_name set (each time hitting a PG
// GetStickerSetByShortName), so the not-found result is cached here and short-circuits within the
// TTL without querying the DB again. The TTL is short (self-healing): a set added by operations or
// at runtime is resolved after at most the TTL, so the negative cache does not hide a real set for
// long.
type stickerSetNegativeCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]time.Time
}

const stickerSetNegativeCacheMaxEntries = 100000

func newStickerSetNegativeCache(ttl time.Duration) *stickerSetNegativeCache {
	return &stickerSetNegativeCache{ttl: ttl, entries: map[string]time.Time{}}
}

func stickerSetRefKey(ref domain.StickerSetRef) string {
	switch ref.Kind {
	case domain.StickerSetRefByID:
		return "id:" + strconv.FormatInt(ref.ID, 10)
	case domain.StickerSetRefByShortName:
		return "short:" + ref.ShortName
	case domain.StickerSetRefBySystem:
		return "sys:" + ref.SystemKey
	default:
		return ""
	}
}

func (c *stickerSetNegativeCache) has(ref domain.StickerSetRef) bool {
	key := stickerSetRefKey(ref)
	if key == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.entries[key]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(c.entries, key)
		return false
	}
	return true
}

func (c *stickerSetNegativeCache) put(ref domain.StickerSetRef) {
	key := stickerSetRefKey(ref)
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// A simple cap prevents unbounded growth: over the limit the whole table is cleared (with a short TTL the cold-start cost is negligible).
	if len(c.entries) >= stickerSetNegativeCacheMaxEntries {
		c.entries = make(map[string]time.Time, 1024)
	}
	c.entries[key] = time.Now().Add(c.ttl)
}

func (c *stickerSetNegativeCache) delete(refs ...domain.StickerSetRef) {
	if c == nil || len(refs) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ref := range refs {
		if key := stickerSetRefKey(ref); key != "" {
			delete(c.entries, key)
		}
	}
}

// blobMetaCache is the in-process LRU of location_key → FileBlob metadata, eliminating a PG
// round-trip for GetFileBlob per upload.getFile chunk (a file is fetched with getFile many times at
// ≤512KB/1MB, and popular stickers/reactions/avatars are repeatedly fetched by many users).
//
// FileBlob metadata is small (about a hundred bytes) and immutable: once written, a location_key
// always points to the same object_key, and a new blob generates a location_key with a random id
// that does not collide with cached entries, so it is fill-only and needs no invalidation.
type blobMetaCache struct {
	mu  sync.Mutex
	cap int
	ll  *list.List
	m   map[string]*list.Element
}

type blobMetaEntry struct {
	key  string
	blob domain.FileBlob
}

func newBlobMetaCache(capacity int) *blobMetaCache {
	if capacity <= 0 {
		capacity = 1
	}
	return &blobMetaCache{
		cap: capacity,
		ll:  list.New(),
		m:   make(map[string]*list.Element, capacity),
	}
}

// get returns the cached FileBlob and moves it to the LRU head; returns ok=false on a miss.
func (c *blobMetaCache) get(key string) (domain.FileBlob, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*blobMetaEntry).blob, true
	}
	return domain.FileBlob{}, false
}

// put writes/updates the cache, evicting the least recently used entry when over capacity.
func (c *blobMetaCache) put(key string, blob domain.FileBlob) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		el.Value.(*blobMetaEntry).blob = blob
		c.ll.MoveToFront(el)
		return
	}
	c.m[key] = c.ll.PushFront(&blobMetaEntry{key: key, blob: blob})
	if c.ll.Len() > c.cap {
		if oldest := c.ll.Back(); oldest != nil {
			c.ll.Remove(oldest)
			delete(c.m, oldest.Value.(*blobMetaEntry).key)
		}
	}
}

// blobBytesCache is the LRU of object_key → full bytes of small blobs. Stickers / reactions /
// thumbnails are usually only a few KB to tens of KB, so caching the full content avoids local-disk
// cold-read jitter when opening history; large media is still read in ranges by BlobBackend.GetRange,
// keeping large files out of memory.
type blobBytesCache struct {
	mu       sync.Mutex
	maxBytes int
	used     int
	ll       *list.List
	m        map[string]*list.Element
}

type blobBytesEntry struct {
	key   string
	bytes []byte
	size  int
}

func newBlobBytesCache(maxBytes int) *blobBytesCache {
	if maxBytes <= 0 {
		maxBytes = 1
	}
	return &blobBytesCache{
		maxBytes: maxBytes,
		ll:       list.New(),
		m:        make(map[string]*list.Element),
	}
}

func (c *blobBytesCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		entry := el.Value.(*blobBytesEntry)
		// Cache entries are immutable after publication. GetFile returns a
		// capacity-clipped read-only view of the requested range.
		return entry.bytes, true
	}
	return nil, false
}

func (c *blobBytesCache) has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		return true
	}
	return false
}

func (c *blobBytesCache) put(key string, bytes []byte) {
	if len(bytes) > c.maxBytes {
		return
	}
	copied := append([]byte(nil), bytes...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		entry := el.Value.(*blobBytesEntry)
		c.used += len(copied) - entry.size
		entry.bytes = copied
		entry.size = len(copied)
		c.ll.MoveToFront(el)
	} else {
		entry := &blobBytesEntry{key: key, bytes: copied, size: len(copied)}
		c.m[key] = c.ll.PushFront(entry)
		c.used += entry.size
	}
	for c.used > c.maxBytes {
		oldest := c.ll.Back()
		if oldest == nil {
			break
		}
		c.ll.Remove(oldest)
		entry := oldest.Value.(*blobBytesEntry)
		delete(c.m, entry.key)
		c.used -= entry.size
	}
}

type stickerSetFullCache struct {
	mu       sync.RWMutex
	epoch    uint64
	byID     map[int64]stickerSetFullEntry
	byShort  map[string]int64
	bySystem map[string]int64
}

type stickerSetFullEntry struct {
	set  domain.StickerSet
	docs []domain.Document
}

func newStickerSetFullCache() *stickerSetFullCache {
	return &stickerSetFullCache{
		byID:     map[int64]stickerSetFullEntry{},
		byShort:  map[string]int64{},
		bySystem: map[string]int64{},
	}
}

func (c *stickerSetFullCache) get(ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var id int64
	switch ref.Kind {
	case domain.StickerSetRefByID:
		id = ref.ID
	case domain.StickerSetRefByShortName:
		id = c.byShort[ref.ShortName]
	case domain.StickerSetRefBySystem:
		id = c.bySystem[ref.SystemKey]
	default:
		return domain.StickerSet{}, nil, false
	}
	entry, ok := c.byID[id]
	if !ok {
		return domain.StickerSet{}, nil, false
	}
	return copyStickerSet(entry.set), copyDocuments(entry.docs), true
}

func (c *stickerSetFullCache) put(set domain.StickerSet, docs []domain.Document) {
	if set.ID == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	c.putLocked(set, docs)
}

func (c *stickerSetFullCache) loadEpoch() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.epoch
}

// putIfEpoch publishes only a snapshot for which no modification/deletion occurred during the read.
// Warm-up and RPC read-cache fills do not advance the epoch, while writes/invalidations do; even if
// the cache is still empty at deletion time, this blocks an older query completing later.
func (c *stickerSetFullCache) putIfEpoch(set domain.StickerSet, docs []domain.Document, epoch uint64) bool {
	if set.ID == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch != epoch {
		return false
	}
	c.putLocked(set, docs)
	return true
}

func (c *stickerSetFullCache) putLocked(set domain.StickerSet, docs []domain.Document) {
	c.byID[set.ID] = stickerSetFullEntry{
		set:  copyStickerSet(set),
		docs: copyDocuments(docs),
	}
	if set.ShortName != "" {
		c.byShort[set.ShortName] = set.ID
	}
	if set.SystemKey != "" {
		c.bySystem[set.SystemKey] = set.ID
	}
}

func (c *stickerSetFullCache) delete(set domain.StickerSet) {
	if c == nil || set.ID == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	delete(c.byID, set.ID)
	if set.ShortName != "" {
		delete(c.byShort, set.ShortName)
	}
	if set.SystemKey != "" {
		delete(c.bySystem, set.SystemKey)
	}
}

func copyStickerSet(set domain.StickerSet) domain.StickerSet {
	set.DocumentIDs = append([]int64(nil), set.DocumentIDs...)
	set.Packs = append([]domain.StickerPack(nil), set.Packs...)
	for i := range set.Packs {
		set.Packs[i].DocumentIDs = append([]int64(nil), set.Packs[i].DocumentIDs...)
	}
	set.Keywords = append([]domain.StickerKeyword(nil), set.Keywords...)
	for i := range set.Keywords {
		set.Keywords[i].Keywords = append([]string(nil), set.Keywords[i].Keywords...)
	}
	set.Thumbs = copyPhotoSizes(set.Thumbs)
	return set
}

func copyDocuments(docs []domain.Document) []domain.Document {
	out := append([]domain.Document(nil), docs...)
	for i := range out {
		out[i].FileReference = append([]byte(nil), out[i].FileReference...)
		out[i].Attributes = append([]domain.DocumentAttribute(nil), out[i].Attributes...)
		for j := range out[i].Attributes {
			out[i].Attributes[j].Waveform = append([]byte(nil), out[i].Attributes[j].Waveform...)
		}
		out[i].Thumbs = copyPhotoSizes(out[i].Thumbs)
	}
	return out
}

func copyPhotoSizes(sizes []domain.PhotoSize) []domain.PhotoSize {
	out := append([]domain.PhotoSize(nil), sizes...)
	for i := range out {
		out[i].Bytes = append([]byte(nil), out[i].Bytes...)
		out[i].Sizes = append([]int(nil), out[i].Sizes...)
	}
	return out
}
