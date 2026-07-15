package mtprotoedge

import (
	"encoding/binary"
	"hash/maphash"
	"sync"
)

const (
	rpcResultSubscriberMaxGlobal    = 1 << 16
	rpcResultSubscriberMaxAuth      = 1 << 13
	rpcResultSubscriberMaxSession   = 1 << 11
	rpcResultSubscriberMaxPerFlight = 1 << 7
)

type rpcResultSubscriberBudgetShard[K comparable] struct {
	mu    sync.Mutex
	usage map[K]int64
}

// rpcResultSubscriberBudget bounds callbacks retained by pending replay
// flights independently from owner/result bytes. A duplicate does not reserve a
// new result row, so charging only unique owners would otherwise leave an
// unbounded same-msg-id reconnect path.
type rpcResultSubscriberBudget struct {
	seed          maphash.Seed
	global        rpcResultFlightLimit
	authLimit     int64
	sessionLimit  int64
	authShards    [rpcResultBudgetShards]rpcResultSubscriberBudgetShard[[8]byte]
	sessionShards [rpcResultBudgetShards]rpcResultSubscriberBudgetShard[rpcResultSessionBudgetKey]
}

func newRPCResultSubscriberBudget(
	seed maphash.Seed,
	globalLimit, authLimit, sessionLimit int,
) *rpcResultSubscriberBudget {
	b := &rpcResultSubscriberBudget{
		seed:         seed,
		authLimit:    int64(authLimit),
		sessionLimit: int64(sessionLimit),
	}
	b.global.max = int64(globalLimit)
	for i := range b.authShards {
		b.authShards[i].usage = make(map[[8]byte]int64)
		b.sessionShards[i].usage = make(map[rpcResultSessionBudgetKey]int64)
	}
	return b
}

func (b *rpcResultSubscriberBudget) reserve(key rpcResultCacheKey, slots int) bool {
	if b == nil || slots <= 0 {
		return false
	}
	authShard := b.authShard(key.authKeyID)
	sessionKey := rpcResultSessionBudgetKey{authKeyID: key.authKeyID, sessionID: key.sessionID}
	sessionShard := b.sessionShard(sessionKey)
	delta := int64(slots)
	authShard.mu.Lock()
	sessionShard.mu.Lock()
	authUsed := authShard.usage[key.authKeyID]
	sessionUsed := sessionShard.usage[sessionKey]
	if !withinRPCResultBudget(authUsed, delta, b.authLimit) ||
		!withinRPCResultBudget(sessionUsed, delta, b.sessionLimit) ||
		!b.global.reserveN(delta) {
		sessionShard.mu.Unlock()
		authShard.mu.Unlock()
		return false
	}
	authShard.usage[key.authKeyID] = authUsed + delta
	sessionShard.usage[sessionKey] = sessionUsed + delta
	sessionShard.mu.Unlock()
	authShard.mu.Unlock()
	return true
}

func (b *rpcResultSubscriberBudget) release(key rpcResultCacheKey, slots int) {
	if b == nil || slots <= 0 {
		panic("mtproto rpc result subscriber release must be positive")
	}
	authShard := b.authShard(key.authKeyID)
	sessionKey := rpcResultSessionBudgetKey{authKeyID: key.authKeyID, sessionID: key.sessionID}
	sessionShard := b.sessionShard(sessionKey)
	delta := int64(slots)
	authShard.mu.Lock()
	sessionShard.mu.Lock()
	authUsed, authOK := authShard.usage[key.authKeyID]
	sessionUsed, sessionOK := sessionShard.usage[sessionKey]
	if !authOK || !sessionOK || authUsed < delta || sessionUsed < delta {
		sessionShard.mu.Unlock()
		authShard.mu.Unlock()
		panic("mtproto rpc result subscriber budget underflow")
	}
	authUsed -= delta
	sessionUsed -= delta
	if authUsed == 0 {
		delete(authShard.usage, key.authKeyID)
	} else {
		authShard.usage[key.authKeyID] = authUsed
	}
	if sessionUsed == 0 {
		delete(sessionShard.usage, sessionKey)
	} else {
		sessionShard.usage[sessionKey] = sessionUsed
	}
	b.global.releaseN(delta)
	sessionShard.mu.Unlock()
	authShard.mu.Unlock()
}

func (b *rpcResultSubscriberBudget) authSnapshot(authKeyID [8]byte) int64 {
	if b == nil {
		return 0
	}
	shard := b.authShard(authKeyID)
	shard.mu.Lock()
	used := shard.usage[authKeyID]
	shard.mu.Unlock()
	return used
}

func (b *rpcResultSubscriberBudget) sessionSnapshot(authKeyID [8]byte, sessionID int64) int64 {
	if b == nil {
		return 0
	}
	key := rpcResultSessionBudgetKey{authKeyID: authKeyID, sessionID: sessionID}
	shard := b.sessionShard(key)
	shard.mu.Lock()
	used := shard.usage[key]
	shard.mu.Unlock()
	return used
}

func (b *rpcResultSubscriberBudget) authShard(
	authKeyID [8]byte,
) *rpcResultSubscriberBudgetShard[[8]byte] {
	index := maphash.Bytes(b.seed, authKeyID[:]) & (rpcResultBudgetShards - 1)
	return &b.authShards[index]
}

func (b *rpcResultSubscriberBudget) sessionShard(
	key rpcResultSessionBudgetKey,
) *rpcResultSubscriberBudgetShard[rpcResultSessionBudgetKey] {
	var raw [16]byte
	copy(raw[:8], key.authKeyID[:])
	binary.LittleEndian.PutUint64(raw[8:], uint64(key.sessionID))
	index := maphash.Bytes(b.seed, raw[:]) & (rpcResultBudgetShards - 1)
	return &b.sessionShards[index]
}
