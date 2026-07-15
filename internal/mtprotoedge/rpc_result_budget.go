package mtprotoedge

import (
	"encoding/binary"
	"hash/maphash"
	"sync"
)

const rpcResultBudgetShards = 64

type rpcResultBudgetLimit struct {
	entries int64
	bytes   int64
}

type rpcResultBudgetUsage struct {
	entries int64
	bytes   int64
	pending int64
}

type rpcResultSessionBudgetKey struct {
	authKeyID [8]byte
	sessionID int64
}

type rpcResultAuthBudgetShard struct {
	mu    sync.Mutex
	usage map[[8]byte]rpcResultBudgetUsage
}

type rpcResultSessionBudgetShard struct {
	mu    sync.Mutex
	usage map[rpcResultSessionBudgetKey]rpcResultBudgetUsage
}

// rpcResultFairBudget accounts one ownership reservation at all three scopes.
// A pending owner and its completed result are the same ownership: admission
// reserves one entry plus one byte, Put resizes that byte reservation and moves
// it to the completed row, while Abort/TTL return the whole reservation.
//
// Auth and session maps are striped independently. Every operation takes the
// auth stripe before the session stripe; global counters remain atomic. This
// keeps unrelated auth keys off one process-wide mutex while preserving hard
// limits at every hierarchy level.
type rpcResultFairBudget struct {
	seed           maphash.Seed
	globalEntries  *rpcResultFlightLimit
	globalBytes    *rpcResultCacheByteBudget
	authLimit      rpcResultBudgetLimit
	sessionLimit   rpcResultBudgetLimit
	pendingPerAuth int64
	authShards     [rpcResultBudgetShards]rpcResultAuthBudgetShard
	sessionShards  [rpcResultBudgetShards]rpcResultSessionBudgetShard
}

type rpcResultBudgetReservation struct {
	budget   *rpcResultFairBudget
	key      rpcResultCacheKey
	bytes    int
	pending  bool
	released bool
}

func newRPCResultFairBudget(
	seed maphash.Seed,
	globalEntries *rpcResultFlightLimit,
	globalBytes *rpcResultCacheByteBudget,
	authLimit rpcResultBudgetLimit,
	sessionLimit rpcResultBudgetLimit,
	pendingPerAuth int,
) *rpcResultFairBudget {
	b := &rpcResultFairBudget{
		seed:           seed,
		globalEntries:  globalEntries,
		globalBytes:    globalBytes,
		authLimit:      authLimit,
		sessionLimit:   sessionLimit,
		pendingPerAuth: int64(pendingPerAuth),
	}
	for i := range b.authShards {
		b.authShards[i].usage = make(map[[8]byte]rpcResultBudgetUsage)
		b.sessionShards[i].usage = make(map[rpcResultSessionBudgetKey]rpcResultBudgetUsage)
	}
	return b
}

func (b *rpcResultFairBudget) reserveOwner(key rpcResultCacheKey) *rpcResultBudgetReservation {
	return b.reserve(key, 1, true)
}

func (b *rpcResultFairBudget) reserveCompleted(key rpcResultCacheKey, bytes int) *rpcResultBudgetReservation {
	return b.reserve(key, bytes, false)
}

func (b *rpcResultFairBudget) reserve(key rpcResultCacheKey, bytes int, pending bool) *rpcResultBudgetReservation {
	if b == nil || b.globalEntries == nil || b.globalBytes == nil || bytes < 1 {
		return nil
	}
	authShard := b.authShard(key.authKeyID)
	sessionKey := rpcResultSessionBudgetKey{authKeyID: key.authKeyID, sessionID: key.sessionID}
	sessionShard := b.sessionShard(sessionKey)
	authShard.mu.Lock()
	sessionShard.mu.Lock()

	authUsage := authShard.usage[key.authKeyID]
	sessionUsage := sessionShard.usage[sessionKey]
	bytes64 := int64(bytes)
	canReserve := withinRPCResultBudget(authUsage.entries, 1, b.authLimit.entries) &&
		withinRPCResultBudget(authUsage.bytes, bytes64, b.authLimit.bytes) &&
		withinRPCResultBudget(sessionUsage.entries, 1, b.sessionLimit.entries) &&
		withinRPCResultBudget(sessionUsage.bytes, bytes64, b.sessionLimit.bytes)
	if pending {
		canReserve = canReserve && withinRPCResultBudget(authUsage.pending, 1, b.pendingPerAuth)
	}
	if !canReserve || !b.globalEntries.reserve() {
		sessionShard.mu.Unlock()
		authShard.mu.Unlock()
		return nil
	}
	if !b.globalBytes.reserve(bytes) {
		b.globalEntries.release()
		sessionShard.mu.Unlock()
		authShard.mu.Unlock()
		return nil
	}
	authUsage.entries++
	authUsage.bytes += bytes64
	sessionUsage.entries++
	sessionUsage.bytes += bytes64
	if pending {
		authUsage.pending++
	}
	authShard.usage[key.authKeyID] = authUsage
	sessionShard.usage[sessionKey] = sessionUsage
	sessionShard.mu.Unlock()
	authShard.mu.Unlock()
	return &rpcResultBudgetReservation{budget: b, key: key, bytes: bytes, pending: pending}
}

func withinRPCResultBudget(used, delta, limit int64) bool {
	return delta >= 0 && limit > 0 && used >= 0 && used <= limit-delta
}

func (r *rpcResultBudgetReservation) resizeBytes(bytes int) bool {
	if r == nil || r.budget == nil || r.released || bytes < 1 {
		return false
	}
	if bytes == r.bytes {
		return true
	}
	b := r.budget
	authShard := b.authShard(r.key.authKeyID)
	sessionKey := rpcResultSessionBudgetKey{authKeyID: r.key.authKeyID, sessionID: r.key.sessionID}
	sessionShard := b.sessionShard(sessionKey)
	authShard.mu.Lock()
	sessionShard.mu.Lock()
	authUsage, authOK := authShard.usage[r.key.authKeyID]
	sessionUsage, sessionOK := sessionShard.usage[sessionKey]
	if !authOK || !sessionOK || authUsage.entries < 1 || sessionUsage.entries < 1 {
		sessionShard.mu.Unlock()
		authShard.mu.Unlock()
		panic("mtprotoedge: rpc result budget reservation disappeared during resize")
	}
	delta := int64(bytes) - int64(r.bytes)
	if delta > 0 {
		if !withinRPCResultBudget(authUsage.bytes, delta, b.authLimit.bytes) ||
			!withinRPCResultBudget(sessionUsage.bytes, delta, b.sessionLimit.bytes) ||
			!b.globalBytes.reserve(int(delta)) {
			sessionShard.mu.Unlock()
			authShard.mu.Unlock()
			return false
		}
	} else {
		if authUsage.bytes < -delta || sessionUsage.bytes < -delta {
			sessionShard.mu.Unlock()
			authShard.mu.Unlock()
			panic("mtprotoedge: rpc result byte reservation underflow during resize")
		}
	}
	authUsage.bytes += delta
	sessionUsage.bytes += delta
	authShard.usage[r.key.authKeyID] = authUsage
	sessionShard.usage[sessionKey] = sessionUsage
	r.bytes = bytes
	if delta < 0 {
		b.globalBytes.release(int(-delta))
	}
	sessionShard.mu.Unlock()
	authShard.mu.Unlock()
	return true
}

func (r *rpcResultBudgetReservation) releasePending() {
	if r == nil || r.budget == nil || r.released || !r.pending {
		return
	}
	b := r.budget
	authShard := b.authShard(r.key.authKeyID)
	authShard.mu.Lock()
	authUsage, ok := authShard.usage[r.key.authKeyID]
	if !ok || authUsage.pending < 1 {
		authShard.mu.Unlock()
		panic("mtprotoedge: rpc result per-auth pending budget underflow")
	}
	authUsage.pending--
	authShard.usage[r.key.authKeyID] = authUsage
	r.pending = false
	authShard.mu.Unlock()
}

func (r *rpcResultBudgetReservation) release() {
	if r == nil || r.budget == nil || r.released {
		return
	}
	b := r.budget
	authShard := b.authShard(r.key.authKeyID)
	sessionKey := rpcResultSessionBudgetKey{authKeyID: r.key.authKeyID, sessionID: r.key.sessionID}
	sessionShard := b.sessionShard(sessionKey)
	authShard.mu.Lock()
	sessionShard.mu.Lock()
	authUsage, authOK := authShard.usage[r.key.authKeyID]
	sessionUsage, sessionOK := sessionShard.usage[sessionKey]
	bytes64 := int64(r.bytes)
	if !authOK || !sessionOK || authUsage.entries < 1 || sessionUsage.entries < 1 ||
		authUsage.bytes < bytes64 || sessionUsage.bytes < bytes64 ||
		(r.pending && authUsage.pending < 1) {
		sessionShard.mu.Unlock()
		authShard.mu.Unlock()
		panic("mtprotoedge: rpc result fair budget underflow")
	}
	authUsage.entries--
	authUsage.bytes -= bytes64
	sessionUsage.entries--
	sessionUsage.bytes -= bytes64
	if r.pending {
		authUsage.pending--
	}
	if authUsage == (rpcResultBudgetUsage{}) {
		delete(authShard.usage, r.key.authKeyID)
	} else {
		authShard.usage[r.key.authKeyID] = authUsage
	}
	if sessionUsage == (rpcResultBudgetUsage{}) {
		delete(sessionShard.usage, sessionKey)
	} else {
		sessionShard.usage[sessionKey] = sessionUsage
	}
	r.released = true
	r.pending = false
	r.bytes = 0
	b.globalBytes.release(int(bytes64))
	b.globalEntries.release()
	sessionShard.mu.Unlock()
	authShard.mu.Unlock()
}

func (b *rpcResultFairBudget) authSnapshot(authKeyID [8]byte) rpcResultBudgetUsage {
	if b == nil {
		return rpcResultBudgetUsage{}
	}
	shard := b.authShard(authKeyID)
	shard.mu.Lock()
	usage := shard.usage[authKeyID]
	shard.mu.Unlock()
	return usage
}

func (b *rpcResultFairBudget) sessionSnapshot(authKeyID [8]byte, sessionID int64) rpcResultBudgetUsage {
	if b == nil {
		return rpcResultBudgetUsage{}
	}
	key := rpcResultSessionBudgetKey{authKeyID: authKeyID, sessionID: sessionID}
	shard := b.sessionShard(key)
	shard.mu.Lock()
	usage := shard.usage[key]
	shard.mu.Unlock()
	return usage
}

func (b *rpcResultFairBudget) authShard(authKeyID [8]byte) *rpcResultAuthBudgetShard {
	index := maphash.Bytes(b.seed, authKeyID[:]) & (rpcResultBudgetShards - 1)
	return &b.authShards[index]
}

func (b *rpcResultFairBudget) sessionShard(key rpcResultSessionBudgetKey) *rpcResultSessionBudgetShard {
	var raw [16]byte
	copy(raw[:8], key.authKeyID[:])
	binary.LittleEndian.PutUint64(raw[8:], uint64(key.sessionID))
	index := maphash.Bytes(b.seed, raw[:]) & (rpcResultBudgetShards - 1)
	return &b.sessionShards[index]
}
