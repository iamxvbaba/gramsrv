package langpack

import (
	"container/list"
	"sync"

	"telesrv/internal/domain"
)

const (
	defaultLangPackCacheMaxBytes       = int64(128 << 20)
	defaultLangPackCacheMaxEntries     = 256
	defaultLanguageListCacheMaxEntries = 32
	langPackStringValueHeaderBytes     = int64(144)
	langPackFixedHeaderBytes           = int64(80)
)

type langPackCacheKind uint8

const (
	langPackCacheRaw langPackCacheKind = iota
	langPackCacheEffective
)

type langPackCacheKey struct {
	pack string
	code string
	kind langPackCacheKind
}

func (k langPackCacheKey) singleflightKey() string {
	return string(rune(k.kind)) + "\x00" + k.pack + "\x00" + k.code
}

type langPackCache struct {
	mu         sync.Mutex
	maxBytes   int64
	maxEntries int
	usedBytes  int64
	epoch      uint64
	ll         *list.List
	items      map[langPackCacheKey]*list.Element
}

type langPackCacheEntry struct {
	key  langPackCacheKey
	pack domain.LangPack
	size int64
}

func newLangPackCache(maxBytes int64, maxEntries int) *langPackCache {
	if maxBytes <= 0 || maxEntries <= 0 {
		return nil
	}
	return &langPackCache{
		maxBytes:   maxBytes,
		maxEntries: maxEntries,
		ll:         list.New(),
		items:      make(map[langPackCacheKey]*list.Element),
	}
}

func (c *langPackCache) get(key langPackCacheKey) (domain.LangPack, bool) {
	if c == nil {
		return domain.LangPack{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[key]
	if !ok {
		return domain.LangPack{}, false
	}
	c.ll.MoveToFront(element)
	return cloneLangPack(element.Value.(*langPackCacheEntry).pack), true
}

func (c *langPackCache) loadEpoch() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	epoch := c.epoch
	c.mu.Unlock()
	return epoch
}

// putIfEpoch 返回 false 仅表示 load 期间发生过 flush，调用方必须重载。
// 超大单项不进入缓存，但仍可安全返回给当前请求，因此返回 true。
func (c *langPackCache) putIfEpoch(key langPackCacheKey, pack domain.LangPack, loadEpoch uint64) bool {
	if c == nil {
		return true
	}
	size := estimateLangPackBytes(pack)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch != loadEpoch {
		return false
	}
	if existing, ok := c.items[key]; ok {
		c.remove(existing)
	}
	if size > c.maxBytes {
		return true
	}
	entry := &langPackCacheEntry{key: key, pack: cloneLangPack(pack), size: size}
	c.items[key] = c.ll.PushFront(entry)
	c.usedBytes += size
	for c.usedBytes > c.maxBytes || c.ll.Len() > c.maxEntries {
		oldest := c.ll.Back()
		if oldest == nil {
			break
		}
		c.remove(oldest)
	}
	return true
}

func (c *langPackCache) flush() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.epoch++
	c.usedBytes = 0
	c.ll.Init()
	clear(c.items)
	c.mu.Unlock()
}

func (c *langPackCache) remove(element *list.Element) {
	entry := element.Value.(*langPackCacheEntry)
	delete(c.items, entry.key)
	c.ll.Remove(element)
	c.usedBytes -= entry.size
}

func estimateLangPackBytes(pack domain.LangPack) int64 {
	size := langPackFixedHeaderBytes + int64(len(pack.LangPack)+len(pack.LangCode))
	size += int64(len(pack.Strings)) * langPackStringValueHeaderBytes
	for _, item := range pack.Strings {
		size += int64(len(item.Key) + len(item.Value) + len(item.ZeroValue) + len(item.OneValue) +
			len(item.TwoValue) + len(item.FewValue) + len(item.ManyValue) + len(item.OtherValue))
	}
	return size
}

func cloneLangPack(pack domain.LangPack) domain.LangPack {
	pack.Strings = append([]domain.LangPackString(nil), pack.Strings...)
	return pack
}

type languageListCache struct {
	mu         sync.Mutex
	maxEntries int
	epoch      uint64
	ll         *list.List
	items      map[string]*list.Element
}

type languageListCacheEntry struct {
	pack      string
	languages []domain.LangPackLanguage
}

func newLanguageListCache(maxEntries int) *languageListCache {
	if maxEntries <= 0 {
		return nil
	}
	return &languageListCache{
		maxEntries: maxEntries,
		ll:         list.New(),
		items:      make(map[string]*list.Element),
	}
}

func (c *languageListCache) get(pack string) ([]domain.LangPackLanguage, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[pack]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(element)
	return cloneLanguages(element.Value.(*languageListCacheEntry).languages), true
}

func (c *languageListCache) loadEpoch() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	epoch := c.epoch
	c.mu.Unlock()
	return epoch
}

func (c *languageListCache) putIfEpoch(pack string, languages []domain.LangPackLanguage, loadEpoch uint64) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch != loadEpoch {
		return false
	}
	if existing, ok := c.items[pack]; ok {
		c.ll.Remove(existing)
		delete(c.items, pack)
	}
	entry := &languageListCacheEntry{pack: pack, languages: cloneLanguages(languages)}
	c.items[pack] = c.ll.PushFront(entry)
	if c.ll.Len() > c.maxEntries {
		oldest := c.ll.Back()
		if oldest != nil {
			delete(c.items, oldest.Value.(*languageListCacheEntry).pack)
			c.ll.Remove(oldest)
		}
	}
	return true
}

func (c *languageListCache) flush() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.epoch++
	c.ll.Init()
	clear(c.items)
	c.mu.Unlock()
}

func cloneLanguages(languages []domain.LangPackLanguage) []domain.LangPackLanguage {
	return append([]domain.LangPackLanguage(nil), languages...)
}
