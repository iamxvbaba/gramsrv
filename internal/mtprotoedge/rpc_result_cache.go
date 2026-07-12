package mtprotoedge

import (
	"container/list"
	"encoding/binary"
	"sync"
	"time"
)

const (
	rpcResultCacheTTL        = 3 * time.Minute
	rpcResultCacheMaxEntries = 4096
	rpcResultCacheMaxBytes   = 64 << 20
	// rpcResultCacheShards 把缓存按 (auth_key_id, session_id) 分片：每条 RPC 都要
	// Get（重复检测）+ Put（结果缓存），单把全局锁会让所有连接的 RPC 热路径在
	// 一个 mutex 上汇聚（同 P0-5 的 SessionManager 教训）。分片数为 2 的幂。
	rpcResultCacheShards = 16
)

type rpcResultCacheKey struct {
	authKeyID [8]byte
	sessionID int64
	reqMsgID  int64
}

type rpcResultCacheEntry struct {
	key       rpcResultCacheKey
	encoded   *encodedOutboundMessage
	size      int
	expiresAt time.Time
}

// rpcResultCache 缓存已有交付证明的 rpc_result（按 auth_key+session+req_msg_id），
// 用于跨连接重放重复请求。Put 的调用方必须先证明结果已物理写出，或原 logical Conn
// 已不可逆 fenced；绝不能发布“Conn 仍 current/open 但结果尚未上 wire”的完成态。
// encodedOutboundMessage 构造后不可变（push fan-out 与 pending resend 均依赖该契约），
// 因此 Get/Put 直接共享指针，不做防御性拷贝。
type rpcResultCache struct {
	shards      [rpcResultCacheShards]rpcResultCacheShard
	flightLimit rpcResultFlightLimit
}

type rpcResultCacheShard struct {
	mu         sync.Mutex
	now        func() time.Time
	ttl        time.Duration
	maxEntries int
	maxBytes   int
	bytes      int
	order      *list.List
	byKey      map[rpcResultCacheKey]*list.Element
	// pending is deliberately independent from the completed-result order/byKey
	// cache. In-flight owners and waiters must not disappear when completed
	// results expire or are trimmed under entry/byte pressure.
	pending map[rpcResultCacheKey]*rpcResultFlight
}

func newRPCResultCacheWithFlightLimit(now func() time.Time, maxPending int) *rpcResultCache {
	if now == nil {
		now = time.Now
	}
	if maxPending <= 0 {
		maxPending = rpcResultFlightDefaultMaxPending
	}
	c := &rpcResultCache{}
	c.flightLimit.max = int64(maxPending)
	for i := range c.shards {
		s := &c.shards[i]
		s.now = now
		s.ttl = rpcResultCacheTTL
		s.maxEntries = rpcResultCacheMaxEntries / rpcResultCacheShards
		s.maxBytes = rpcResultCacheMaxBytes / rpcResultCacheShards
		s.order = list.New()
		s.byKey = make(map[rpcResultCacheKey]*list.Element)
		s.pending = make(map[rpcResultCacheKey]*rpcResultFlight)
	}
	return c
}

func (c *rpcResultCache) shard(key rpcResultCacheKey) *rpcResultCacheShard {
	// auth_key_id 与 session_id 都是均匀随机的 64-bit 值，异或折叠后取低位即可。
	h := binary.LittleEndian.Uint64(key.authKeyID[:]) ^ uint64(key.sessionID)
	h ^= h >> 32
	return &c.shards[h&(rpcResultCacheShards-1)]
}

func (c *rpcResultCache) Get(authKeyID [8]byte, sessionID, reqMsgID int64) (*encodedOutboundMessage, bool) {
	if c == nil || reqMsgID == 0 {
		return nil, false
	}
	key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: sessionID, reqMsgID: reqMsgID}
	s := c.shard(key)
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	elem, ok := s.byKey[key]
	if !ok {
		return nil, false
	}
	entry := elem.Value.(*rpcResultCacheEntry)
	if !entry.expiresAt.After(now) {
		s.removeElement(elem)
		return nil, false
	}
	return entry.encoded, true
}

func (c *rpcResultCache) Put(authKeyID [8]byte, sessionID, reqMsgID int64, encoded *encodedOutboundMessage) {
	if c == nil || reqMsgID == 0 || encoded == nil {
		return
	}
	key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: sessionID, reqMsgID: reqMsgID}
	s := c.shard(key)
	size := len(encoded.body)
	cacheable := s.maxBytes <= 0 || size <= s.maxBytes
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if cacheable {
		s.expireLocked(now)
		if elem, ok := s.byKey[key]; ok {
			s.removeElement(elem)
		}
		entry := &rpcResultCacheEntry{
			key:       key,
			encoded:   encoded,
			size:      size,
			expiresAt: now.Add(s.ttl),
		}
		elem := s.order.PushBack(entry)
		s.byKey[key] = elem
		s.bytes += size
		s.trimLocked()
	}
	// Resolve the independent in-flight entry only after the completed cache has
	// been published. Waiters awakened by this close can therefore immediately
	// observe either the shared encoded result or the completed Get entry.
	c.completeRPCResultFlightLocked(s, key, encoded)
}

func (s *rpcResultCacheShard) expireLocked(now time.Time) {
	for elem := s.order.Front(); elem != nil; {
		next := elem.Next()
		entry := elem.Value.(*rpcResultCacheEntry)
		if entry.expiresAt.After(now) {
			return
		}
		s.removeElement(elem)
		elem = next
	}
}

func (s *rpcResultCacheShard) trimLocked() {
	for s.order.Len() > 0 {
		tooManyEntries := s.maxEntries > 0 && s.order.Len() > s.maxEntries
		tooManyBytes := s.maxBytes > 0 && s.bytes > s.maxBytes
		if !tooManyEntries && !tooManyBytes {
			return
		}
		s.removeElement(s.order.Front())
	}
}

func (s *rpcResultCacheShard) removeElement(elem *list.Element) {
	if elem == nil {
		return
	}
	entry := elem.Value.(*rpcResultCacheEntry)
	delete(s.byKey, entry.key)
	s.bytes -= entry.size
	if s.bytes < 0 {
		s.bytes = 0
	}
	s.order.Remove(elem)
}
