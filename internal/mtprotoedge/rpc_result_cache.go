package mtprotoedge

import (
	"container/list"
	"encoding/binary"
	"hash/maphash"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// Telegram accepts client msg_id values up to five minutes old and up to
	// thirty seconds in the future. Retain the result across that complete
	// replay horizon, plus one second for boundary/scheduler jitter, so a valid
	// duplicate cannot rerun its handler merely because our cache expired first.
	rpcResultCacheTTL = 331 * time.Second
	// Completed results cover the complete replay horizon under explicit global,
	// auth and session hard ceilings. At the default 331-second TTL, the 1<<18
	// global entries permit about 792 unique RPC/s process-wide before bounded
	// backpressure; lower scopes provide noisy-neighbor isolation.
	rpcResultCacheMaxEntries         = 1 << 18
	rpcResultCacheMaxBytes           = 64 << 20
	rpcResultCacheAuthMaxEntries     = 1 << 15
	rpcResultCacheAuthMaxBytes       = 32 << 20
	rpcResultCacheSessionMaxEntries  = 1 << 14
	rpcResultCacheSessionMaxBytes    = 16 << 20
	rpcResultFlightMaxPendingPerAuth = 1 << 11
	// Keep every transport-legal rpc_result cacheable. Converting the constant
	// difference to uint64 intentionally fails compilation if a future transport
	// limit grows beyond the completed-result budget.
	_ = uint64(rpcResultCacheMaxBytes - maxOutboundBodyBytes)
	_ = uint64(rpcResultCacheAuthMaxBytes - maxOutboundBodyBytes)
	_ = uint64(rpcResultCacheSessionMaxBytes - maxOutboundBodyBytes)
	// rpcResultCacheShards hashes the complete replay identity with a random
	// per-instance maphash seed. Including req_msg_id spreads one hot session's
	// independent requests instead of forcing them through one mutex. The shard
	// count is a power of two.
	rpcResultCacheShards = 16
)

type rpcResultCacheKey struct {
	authKeyID [8]byte
	sessionID int64
	reqMsgID  int64
}

type rpcResultCacheEntry struct {
	key            rpcResultCacheKey
	encoded        *encodedOutboundMessage
	size           int
	expiresAt      time.Time
	identity       rpcResultRequestIdentity
	admissionSeq   uint64
	executionKnown bool
	executionOK    bool
	// capacity marks a bounded replay tombstone. The original owner and its
	// already-joined waiters received encoded, but the byte budget could not
	// retain that body. Keeping the immutable identity until TTL prevents a
	// duplicate from rerunning business; Acquire returns a capacity error.
	capacity bool
	// reservation is the same global+auth+session ownership acquired before the
	// handler ran. Put transfers it from the pending flight; TTL returns it.
	reservation *rpcResultBudgetReservation
}

type rpcResultDependency struct {
	waiter    *rpcResultWaiter
	completed bool
	success   bool
}

// rpcResultCache 缓存已有交付证明的 rpc_result（按 auth_key+session+req_msg_id），
// 用于跨连接重放重复请求。Put 的调用方必须先证明结果已物理写出，或原 logical Conn
// 已不可逆 fenced；绝不能发布“Conn 仍 current/open 但结果尚未上 wire”的完成态。
// encodedOutboundMessage 构造后不可变（push fan-out 与 pending resend 均依赖该契约），
// 因此 Get/Put 直接共享指针，不做防御性拷贝。
type rpcResultCache struct {
	shards              [rpcResultCacheShards]rpcResultCacheShard
	hashSeed            maphash.Seed
	completedBytes      rpcResultCacheByteBudget
	completedEntries    rpcResultFlightLimit
	fairBudget          *rpcResultFairBudget
	flightLimit         rpcResultFlightLimit
	subscriberBudget    *rpcResultSubscriberBudget
	subscriberPerFlight int
	// nextAdmissionSeq is the process-wide ordering authority for auth-key
	// shared Layer defaults. Exact owners allocate once; joins/replays retain the
	// owner's value from their flight/completed descriptor.
	nextAdmissionSeq atomic.Uint64
	activeAdmissions rpcAdmissionTracker
}

func (c *rpcResultCache) stableAdmissionSafeFloor() uint64 {
	if c == nil {
		return 0
	}
	return c.activeAdmissions.stableSafeFloor(&c.nextAdmissionSeq)
}

type rpcResultCacheShard struct {
	mu  sync.Mutex
	now func() time.Time
	ttl time.Duration
	// maxEntries is a focused-test seam for one physical shard. Production leaves
	// it zero and uses the explicit global/auth/session fair-budget hierarchy.
	maxEntries int
	bytes      int
	order      *list.List
	byKey      map[rpcResultCacheKey]*list.Element
	// pending is deliberately independent from the completed-result order/byKey
	// cache. In-flight owners and waiters must not disappear when completed
	// results expire or are trimmed under entry/byte pressure.
	pending map[rpcResultCacheKey]*rpcResultFlight
}

func newRPCResultCacheWithFlightLimit(now func() time.Time, maxPending int) *rpcResultCache {
	if maxPending <= 0 {
		maxPending = rpcResultFlightDefaultMaxPending
	}
	pendingPerAuth := rpcResultFlightMaxPendingPerAuth
	if pendingPerAuth > maxPending {
		pendingPerAuth = maxPending
	}
	return newRPCResultCacheWithFairCapacity(now, rpcResultCacheCapacity{
		maxPending:        maxPending,
		maxPendingPerAuth: pendingPerAuth,
		globalMaxBytes:    rpcResultCacheMaxBytes,
		globalMaxEntries:  rpcResultCacheMaxEntries,
		authMaxBytes:      rpcResultCacheAuthMaxBytes,
		authMaxEntries:    rpcResultCacheAuthMaxEntries,
		sessionMaxBytes:   rpcResultCacheSessionMaxBytes,
		sessionMaxEntries: rpcResultCacheSessionMaxEntries,
	})
}

func newRPCResultCacheWithLimits(now func() time.Time, maxPending, maxCompletedBytes int) *rpcResultCache {
	return newRPCResultCacheWithCapacity(now, maxPending, int64(maxCompletedBytes), rpcResultCacheMaxEntries)
}

func newRPCResultCacheWithCapacity(
	now func() time.Time,
	maxPending int,
	maxCompletedBytes int64,
	maxCompletedEntries int,
) *rpcResultCache {
	// Compatibility/test constructor: the caller supplied only global limits, so
	// keep every fairness scope equal to that global ceiling. Production always
	// calls newRPCResultCacheWithFairCapacity with explicit auth/session limits.
	return newRPCResultCacheWithFairCapacity(now, rpcResultCacheCapacity{
		maxPending:        maxPending,
		maxPendingPerAuth: maxPending,
		globalMaxBytes:    maxCompletedBytes,
		globalMaxEntries:  maxCompletedEntries,
		authMaxBytes:      maxCompletedBytes,
		authMaxEntries:    maxCompletedEntries,
		sessionMaxBytes:   maxCompletedBytes,
		sessionMaxEntries: maxCompletedEntries,
	})
}

type rpcResultCacheCapacity struct {
	maxPending             int
	maxPendingPerAuth      int
	globalMaxBytes         int64
	globalMaxEntries       int
	authMaxBytes           int64
	authMaxEntries         int
	sessionMaxBytes        int64
	sessionMaxEntries      int
	subscriberMaxGlobal    int
	subscriberMaxAuth      int
	subscriberMaxSession   int
	subscriberMaxPerFlight int
}

func newRPCResultCacheWithFairCapacity(now func() time.Time, capacity rpcResultCacheCapacity) *rpcResultCache {
	if now == nil {
		now = time.Now
	}
	if capacity.maxPending <= 0 {
		capacity.maxPending = rpcResultFlightDefaultMaxPending
	}
	if capacity.maxPendingPerAuth <= 0 {
		capacity.maxPendingPerAuth = capacity.maxPending
	}
	if capacity.globalMaxBytes <= 0 {
		capacity.globalMaxBytes = rpcResultCacheMaxBytes
	}
	if capacity.globalMaxEntries <= 0 {
		capacity.globalMaxEntries = rpcResultCacheMaxEntries
	}
	if capacity.authMaxBytes <= 0 {
		capacity.authMaxBytes = capacity.globalMaxBytes
	}
	if capacity.authMaxEntries <= 0 {
		capacity.authMaxEntries = capacity.globalMaxEntries
	}
	if capacity.sessionMaxBytes <= 0 {
		capacity.sessionMaxBytes = capacity.authMaxBytes
	}
	if capacity.sessionMaxEntries <= 0 {
		capacity.sessionMaxEntries = capacity.authMaxEntries
	}
	if capacity.subscriberMaxGlobal <= 0 {
		capacity.subscriberMaxGlobal = rpcResultSubscriberMaxGlobal
	}
	if capacity.subscriberMaxAuth <= 0 {
		capacity.subscriberMaxAuth = rpcResultSubscriberMaxAuth
	}
	if capacity.subscriberMaxSession <= 0 {
		capacity.subscriberMaxSession = rpcResultSubscriberMaxSession
	}
	if capacity.subscriberMaxPerFlight <= 0 {
		capacity.subscriberMaxPerFlight = rpcResultSubscriberMaxPerFlight
	}
	c := &rpcResultCache{hashSeed: maphash.MakeSeed()}
	c.completedBytes.max = capacity.globalMaxBytes
	c.completedEntries.max = int64(capacity.globalMaxEntries)
	c.flightLimit.max = int64(capacity.maxPending)
	c.fairBudget = newRPCResultFairBudget(
		c.hashSeed,
		&c.completedEntries,
		&c.completedBytes,
		rpcResultBudgetLimit{entries: int64(capacity.authMaxEntries), bytes: capacity.authMaxBytes},
		rpcResultBudgetLimit{entries: int64(capacity.sessionMaxEntries), bytes: capacity.sessionMaxBytes},
		capacity.maxPendingPerAuth,
	)
	c.subscriberBudget = newRPCResultSubscriberBudget(
		c.hashSeed,
		capacity.subscriberMaxGlobal,
		capacity.subscriberMaxAuth,
		capacity.subscriberMaxSession,
	)
	c.subscriberPerFlight = capacity.subscriberMaxPerFlight
	for i := range c.shards {
		s := &c.shards[i]
		s.now = now
		s.ttl = rpcResultCacheTTL
		s.maxEntries = 0
		s.order = list.New()
		s.byKey = make(map[rpcResultCacheKey]*list.Element)
		s.pending = make(map[rpcResultCacheKey]*rpcResultFlight)
	}
	return c
}

func (c *rpcResultCache) shard(key rpcResultCacheKey) *rpcResultCacheShard {
	return &c.shards[c.shardIndex(key)]

}

func (c *rpcResultCache) shardIndex(key rpcResultCacheKey) uint64 {
	var raw [24]byte
	copy(raw[:8], key.authKeyID[:])
	binary.LittleEndian.PutUint64(raw[8:16], uint64(key.sessionID))
	binary.LittleEndian.PutUint64(raw[16:24], uint64(key.reqMsgID))
	return maphash.Bytes(c.hashSeed, raw[:]) & (rpcResultCacheShards - 1)
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
	if entry.capacity || entry.encoded == nil {
		return nil, false
	}
	return entry.encoded, true
}

// ObserveDependency returns a waiter for an admitted in-flight dependency, a
// nil waiter for an already completed dependency, or ok=false when the
// referenced message never established API-RPC ownership. It never creates a
// flight and therefore cannot turn a forged invokeAfterMsg into authority to
// run another request.
func (c *rpcResultCache) ObserveDependency(authKeyID [8]byte, sessionID, reqMsgID int64) (rpcResultDependency, bool) {
	if c == nil || reqMsgID == 0 {
		return rpcResultDependency{}, false
	}
	key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: sessionID, reqMsgID: reqMsgID}
	s := c.shard(key)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if elem, exists := s.byKey[key]; exists {
		entry := elem.Value.(*rpcResultCacheEntry)
		if entry.expiresAt.After(now) {
			if !entry.executionKnown {
				return rpcResultDependency{}, false
			}
			return rpcResultDependency{completed: true, success: entry.executionOK}, true
		}
		s.removeElement(elem)
	}
	if flight := s.pending[key]; flight != nil {
		if flight.executionDone {
			return rpcResultDependency{completed: true, success: flight.executionOK}, true
		}
		return rpcResultDependency{waiter: &rpcResultWaiter{cache: c, key: key, flight: flight}}, true
	}
	return rpcResultDependency{}, false
}

func (c *rpcResultCache) Put(authKeyID [8]byte, sessionID, reqMsgID int64, encoded *encodedOutboundMessage) {
	if c == nil || reqMsgID == 0 || encoded == nil {
		return
	}
	if c.putOnce(authKeyID, sessionID, reqMsgID, encoded) {
		return
	}
	// A direct Put has no pre-reserved owner slot. Expired entries in another
	// shard may be its only blocker; reap once without holding a shard and retry.
	// Production owner publication already carries both reservations and never
	// needs this cold path.
	c.expireCompletedResults()
	_ = c.putOnce(authKeyID, sessionID, reqMsgID, encoded)
}

// putOnce returns false only when a cross-shard expiry reap may release the
// process-wide entry/body capacity needed by a defensive direct Put.
func (c *rpcResultCache) putOnce(authKeyID [8]byte, sessionID, reqMsgID int64, encoded *encodedOutboundMessage) bool {
	key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: sessionID, reqMsgID: reqMsgID}
	s := c.shard(key)
	accountedSize := len(encoded.body)
	if accountedSize < 1 {
		// Every owner reserves one byte at admission. Keeping zero-length results
		// at the same minimum makes entry and byte capacity linearizable.
		accountedSize = 1
	}

	// Publication never evicts another unexpired result. A production owner has
	// already reserved its entry slot and one byte. If its actual result cannot
	// expand that reservation, publish a one-byte identity tombstone: the owner
	// and current waiters still receive the immutable result, while later
	// duplicates fail admission instead of rerunning the handler.
	s.mu.Lock()
	now := s.now()
	s.expireLocked(now)
	old := s.byKey[key]
	flight := s.pending[key]
	if old == nil && flight == nil && s.maxEntries > 0 && len(s.byKey) >= s.maxEntries {
		// Defensive direct Put callers do not own a reserved admission slot.
		// Preserve every existing unexpired result and decline the new cache row.
		s.mu.Unlock()
		return true
	}

	identity, admissionSeq, executionKnown, executionOK := rpcResultFlightMetadataLocked(s, key)
	var oldEntry *rpcResultCacheEntry
	if old != nil {
		oldEntry = old.Value.(*rpcResultCacheEntry)
		if flight == nil {
			// A defensive duplicate terminal publication must never downgrade
			// completed dependency/identity metadata after its flight disappeared.
			identity = oldEntry.identity
			admissionSeq = oldEntry.admissionSeq
			executionKnown = oldEntry.executionKnown
			executionOK = oldEntry.executionOK
		}
	}

	var reservation *rpcResultBudgetReservation
	switch {
	case flight != nil:
		reservation = flight.reservation
		if reservation == nil {
			s.mu.Unlock()
			panic("mtprotoedge: pending rpc result has no fair-budget reservation")
		}
	case oldEntry != nil && oldEntry.reservation != nil:
		reservation = oldEntry.reservation
	default:
		reservation = c.fairBudget.reserveCompleted(key, accountedSize)
		if reservation == nil {
			s.mu.Unlock()
			return false
		}
	}

	retainedSize := accountedSize
	retained := encoded
	capacity := false
	if !reservation.resizeBytes(accountedSize) {
		if flight == nil {
			// A direct replacement cannot discard the prior replay body. Leave it
			// untouched and let Put perform one cross-shard expiry reap before its
			// final bounded failure.
			if oldEntry == nil {
				reservation.release()
			}
			s.mu.Unlock()
			return false
		}
		// Owner admission already reserved one byte at all three scopes. When the
		// actual body cannot expand, transfer that reservation to an identity
		// tombstone so a duplicate never reruns business.
		const tombstoneSize = 1
		if !reservation.resizeBytes(tombstoneSize) {
			s.mu.Unlock()
			panic("mtprotoedge: rpc result owner lost its one-byte tombstone reservation")
		}
		retainedSize = tombstoneSize
		retained = nil
		capacity = true
	}

	if old != nil {
		s.unlinkElement(old)
		if oldEntry.reservation != nil && oldEntry.reservation != reservation {
			oldEntry.reservation.release()
			oldEntry.reservation = nil
		}
	}
	entry := &rpcResultCacheEntry{
		key:            key,
		encoded:        retained,
		size:           retainedSize,
		expiresAt:      now.Add(s.ttl),
		identity:       identity,
		admissionSeq:   admissionSeq,
		executionKnown: executionKnown,
		executionOK:    executionOK,
		capacity:       capacity,
		reservation:    reservation,
	}
	elem := s.order.PushBack(entry)
	s.byKey[key] = elem
	s.bytes += retainedSize

	// Resolve the independent in-flight entry only after either the completed
	// result or its replay tombstone is published under the same shard lock.
	subscribers, executionSubscribers, executionOK := c.completeRPCResultFlightLocked(s, key, encoded)
	s.mu.Unlock()
	for _, subscriber := range subscribers {
		subscriber(encoded, true)
	}
	for _, subscriber := range executionSubscribers {
		subscriber(executionOK)
	}
	return true
}

func rpcResultFlightMetadataLocked(s *rpcResultCacheShard, key rpcResultCacheKey) (
	rpcResultRequestIdentity,
	uint64,
	bool,
	bool,
) {
	if flight := s.pending[key]; flight != nil {
		return flight.identity, flight.admissionSeq, flight.executionDone, flight.executionOK
	}
	return rpcResultRequestIdentity{}, 0, false, false
}

// expireCompletedResults performs the cold-path cross-shard reap used only
// after a one-byte admission reservation fails. The caller must hold no shard
// lock. Each shard is reaped independently so ordinary result publication on
// the other shards remains parallel.
func (c *rpcResultCache) expireCompletedResults() {
	if c == nil {
		return
	}
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		s.expireLocked(s.now())
		s.mu.Unlock()
	}
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

func (s *rpcResultCacheShard) removeElement(elem *list.Element) {
	entry := s.unlinkElement(elem)
	if entry != nil && entry.reservation != nil {
		entry.reservation.release()
		entry.reservation = nil
	}
}

func (s *rpcResultCacheShard) unlinkElement(elem *list.Element) *rpcResultCacheEntry {
	if elem == nil {
		return nil
	}
	entry := elem.Value.(*rpcResultCacheEntry)
	delete(s.byKey, entry.key)
	s.bytes -= entry.size
	if s.bytes < 0 {
		s.bytes = 0
	}
	s.order.Remove(elem)
	return entry
}

type rpcResultCacheByteBudget struct {
	max  int64
	used atomic.Int64
}

func (b *rpcResultCacheByteBudget) reserve(n int) bool {
	if n <= 0 {
		return true
	}
	bytes := int64(n)
	if b == nil || bytes > b.max {
		return false
	}
	for {
		used := b.used.Load()
		if used > b.max-bytes {
			return false
		}
		if b.used.CompareAndSwap(used, used+bytes) {
			return true
		}
	}
}

func (b *rpcResultCacheByteBudget) release(n int) {
	if b == nil || n <= 0 {
		return
	}
	if remaining := b.used.Add(-int64(n)); remaining < 0 {
		panic("mtprotoedge: rpc result completed-byte budget underflow")
	}
}

func (b *rpcResultCacheByteBudget) snapshot() int64 {
	if b == nil {
		return 0
	}
	return b.used.Load()
}
