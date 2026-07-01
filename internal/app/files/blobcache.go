package files

import (
	"container/list"
	"strconv"
	"sync"
	"time"

	"telesrv/internal/domain"
)

// stickerSetNegativeCache 是「按 ref 查不到的贴纸集」的短 TTL 负缓存。未 seed 的 short_name
// 集合会被客户端反复 getStickerSet（每次都打一发 PG GetStickerSetByShortName），这里缓存
// not-found 结果，TTL 内直接短路、不再查库。TTL 短（自愈）：运营/运行时新增的集合最多 TTL 后
// 才被解析，避免负缓存长期遮住真实存在的集合。
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
	// 简单上限防无界增长：超限整表清空（短 TTL 下冷启代价可忽略）。
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

// blobMetaCache 是 location_key → FileBlob 元数据的进程内 LRU，用于消除 upload.getFile
// 每个 chunk 一次 GetFileBlob 的 PG 往返（一个文件按 ≤512KB/1MB 分多次 getFile，热门贴纸/
// reaction/头像更被大量用户重复拉）。
//
// FileBlob 元数据小（约百字节）且内容不可变：location_key 一旦写入即固定指向同一 object_key，
// 新建 blob 用随机 id 生成 location_key 不会与已缓存项冲突，故只读填充、无需失效。
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

// get 返回缓存的 FileBlob 并把其移到 LRU 头部；未命中返回 ok=false。
func (c *blobMetaCache) get(key string) (domain.FileBlob, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*blobMetaEntry).blob, true
	}
	return domain.FileBlob{}, false
}

// put 写入/更新缓存，超出容量时淘汰最久未用项。
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

// blobBytesCache 是 object_key → 小 blob 全量字节的 LRU。Sticker / reaction /
// 缩略图通常只有几 KB 到几十 KB，缓存全量内容可以避开点击历史时的本地磁盘冷读抖动；
// 大媒体仍由 BlobBackend.GetRange 分段读取，避免把大文件放进内存。
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
		return append([]byte(nil), entry.bytes...), true
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
