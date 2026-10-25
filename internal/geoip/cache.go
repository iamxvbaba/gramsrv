package geoip

import (
	"net/netip"
	"sync"
	"time"
)

// cacheEntry 是一条缓存记录。resolved=false 是负缓存:后端确实被问过了,
// 但没有给出可用结果(查无此 IP、限流、超时、网络错误)。
type cacheEntry struct {
	location Location
	resolved bool
	expires  time.Time
}

type cacheConfig struct {
	ttl     time.Duration
	negTTL  time.Duration
	maxSize int
}

// cache 是带 TTL 的定容 IP 缓存,命中和未命中都会记。
//
// 负缓存是这个后端能不能用的关键:限流窗口内 reallyfreegeoip.org 对同一个查不到的
// IP 会反复返回同样的空结果,不缓存就等于每打开一次会话列表都重付一遍 0.7s,而
// 这些请求又会把 429 窗口续得更久——负反馈会把限流从偶发变成常态。
type cache struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	negTTL  time.Duration
	maxSize int
	entries map[netip.Addr]cacheEntry
}

func newCache(cfg cacheConfig) *cache {
	return &cache{
		now:     time.Now,
		ttl:     positiveDuration(cfg.ttl, defaultCacheTTL),
		negTTL:  positiveDuration(cfg.negTTL, defaultNegativeTTL),
		maxSize: positiveInt(cfg.maxSize, defaultCacheSize),
		entries: make(map[netip.Addr]cacheEntry),
	}
}

// get 返回未过期的条目。第二个值表示"缓存里有这条记录",即使它是一条负缓存。
func (c *cache) get(addr netip.Addr) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[addr]
	if !ok {
		return cacheEntry{}, false
	}
	if !c.now().Before(entry.expires) {
		delete(c.entries, addr)
		return cacheEntry{}, false
	}
	return entry, true
}

// put 记录一次成功解析。正文有效期用长 TTL——单个 IP 的地理归属基本不变,
// 没必要为了时效性反复去换一个本来也不会变的结果。
func (c *cache) put(addr netip.Addr, loc Location) {
	c.set(addr, cacheEntry{location: loc, resolved: true, expires: c.now().Add(c.ttl)})
}

// putMiss 记录一次没有结果。有效期用短 TTL,否则一次限流会把该 IP 锁到第二天。
func (c *cache) putMiss(addr netip.Addr) {
	c.set(addr, cacheEntry{resolved: false, expires: c.now().Add(c.negTTL)})
}

func (c *cache) set(addr netip.Addr, entry cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 覆盖已有条目时先摘掉过期项,否则一个高频轮换的地址集合会把过期项一直
	// 挤在 map 里,提前触发下面的整体清空。
	if existing, ok := c.entries[addr]; ok && !c.now().Before(existing.expires) {
		delete(c.entries, addr)
	}
	c.entries[addr] = entry
	c.evictLocked()
}

// evictLocked 保证条目数不超过 maxSize。
//
// 先清过期项;仍然超限就整体清空。会话 IP 的基数本来就随在线用户增长,而一次
// 清理最多让下一批请求重查一次(约 0.7s),换来的是一个不需要维护 LRU 链表的
// 确定性内存上界——对一个展示用途的缓存,这个取舍是划算的。
func (c *cache) evictLocked() {
	if len(c.entries) <= c.maxSize {
		return
	}
	now := c.now()
	for addr, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, addr)
		}
	}
	if len(c.entries) <= c.maxSize {
		return
	}
	clear(c.entries)
}
