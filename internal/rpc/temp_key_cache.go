package rpc

import (
	"container/list"
	"sync"
	"time"
)

const defaultTempKeyResolveCacheMaxEntries = 262144

type tempKeyResolveEntry struct {
	perm     [8]byte
	expireAt time.Time
}

type tempKeyResolveCacheItem struct {
	raw   [8]byte
	entry tempKeyResolveEntry
}

type tempKeyResolveCache struct {
	mu      sync.Mutex
	max     int
	entries map[[8]byte]*list.Element
	byPerm  map[[8]byte]map[[8]byte]struct{}
	order   *list.List
}

func newTempKeyResolveCache(maxEntries int) *tempKeyResolveCache {
	if maxEntries <= 0 {
		maxEntries = defaultTempKeyResolveCacheMaxEntries
	}
	return &tempKeyResolveCache{
		max: maxEntries,
		// maxEntries is an eviction ceiling, not an expected steady-state population.  A
		// capacity hint of 262k eagerly reserves a large hash table for every Router even when
		// PFS/temp keys are never used; let the map grow lazily with actual bindings instead.
		entries: make(map[[8]byte]*list.Element),
		byPerm:  make(map[[8]byte]map[[8]byte]struct{}),
		order:   list.New(),
	}
}

func (c *tempKeyResolveCache) Get(rawAuthKeyID, expectedPermAuthKeyID [8]byte, now time.Time) ([8]byte, bool) {
	if c == nil {
		return [8]byte{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el := c.entries[rawAuthKeyID]
	if el == nil {
		return [8]byte{}, false
	}
	item := el.Value.(tempKeyResolveCacheItem)
	if !item.entry.expireAt.After(now) || item.entry.perm != expectedPermAuthKeyID {
		c.removeElementLocked(el)
		return [8]byte{}, false
	}
	c.order.MoveToBack(el)
	return item.entry.perm, true
}

func (c *tempKeyResolveCache) Store(rawAuthKeyID, permAuthKeyID [8]byte, expireAt, _ time.Time) {
	if c == nil || c.max <= 0 || rawAuthKeyID == ([8]byte{}) || permAuthKeyID == ([8]byte{}) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el := c.entries[rawAuthKeyID]; el != nil {
		old := el.Value.(tempKeyResolveCacheItem)
		if old.entry.perm != permAuthKeyID {
			c.removeReverseLocked(old.raw, old.entry.perm)
			c.addReverseLocked(rawAuthKeyID, permAuthKeyID)
		}
		el.Value = tempKeyResolveCacheItem{raw: rawAuthKeyID, entry: tempKeyResolveEntry{perm: permAuthKeyID, expireAt: expireAt}}
		c.order.MoveToBack(el)
		return
	}
	el := c.order.PushBack(tempKeyResolveCacheItem{raw: rawAuthKeyID, entry: tempKeyResolveEntry{perm: permAuthKeyID, expireAt: expireAt}})
	c.entries[rawAuthKeyID] = el
	c.addReverseLocked(rawAuthKeyID, permAuthKeyID)
	for len(c.entries) > c.max {
		c.removeElementLocked(c.order.Front())
	}
}

func (c *tempKeyResolveCache) Delete(rawAuthKeyID [8]byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el := c.entries[rawAuthKeyID]; el != nil {
		c.removeElementLocked(el)
	}
}

func (c *tempKeyResolveCache) DeleteByPerm(permAuthKeyID [8]byte) [][8]byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	raws := c.byPerm[permAuthKeyID]
	rawAuthKeyIDs := make([][8]byte, 0, len(raws))
	for raw := range raws {
		rawAuthKeyIDs = append(rawAuthKeyIDs, raw)
		if el := c.entries[raw]; el != nil {
			c.removeElementLocked(el)
		}
	}
	return rawAuthKeyIDs
}

func (c *tempKeyResolveCache) removeElementLocked(el *list.Element) {
	if el == nil {
		return
	}
	item := el.Value.(tempKeyResolveCacheItem)
	delete(c.entries, item.raw)
	c.removeReverseLocked(item.raw, item.entry.perm)
	c.order.Remove(el)
}

func (c *tempKeyResolveCache) addReverseLocked(raw, perm [8]byte) {
	raws := c.byPerm[perm]
	if raws == nil {
		raws = make(map[[8]byte]struct{})
		c.byPerm[perm] = raws
	}
	raws[raw] = struct{}{}
}

func (c *tempKeyResolveCache) removeReverseLocked(raw, perm [8]byte) {
	raws := c.byPerm[perm]
	if raws == nil {
		return
	}
	delete(raws, raw)
	if len(raws) == 0 {
		delete(c.byPerm, perm)
	}
}
