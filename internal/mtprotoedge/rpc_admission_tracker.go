package mtprotoedge

import (
	"math"
	"sync"
	"sync/atomic"
)

const rpcAdmissionTrackerShards = 64

// rpcAdmissionTracker retains exactly the admission sequences whose unique
// owner can still publish/complete. Allocation holds the barrier from sequence
// CAS through shard registration, so a stable floor scan can never observe an
// allocated-but-not-yet-active gap.
type rpcAdmissionTracker struct {
	allocationBarrier sync.RWMutex
	shards            [rpcAdmissionTrackerShards]rpcAdmissionTrackerShard
}

type rpcAdmissionTrackerShard struct {
	mu     sync.Mutex
	active map[uint64]struct{}
}

func (t *rpcAdmissionTracker) allocateAndRegister(next *atomic.Uint64) (uint64, error) {
	if t == nil || next == nil {
		return 0, ErrRPCResultFlightInvalid
	}
	t.allocationBarrier.RLock()
	defer t.allocationBarrier.RUnlock()
	var sequence uint64
	for {
		current := next.Load()
		if current == math.MaxUint64 {
			return 0, ErrRPCAdmissionSeqExhausted
		}
		sequence = current + 1
		if next.CompareAndSwap(current, sequence) {
			break
		}
	}
	shard := &t.shards[sequence&(rpcAdmissionTrackerShards-1)]
	shard.mu.Lock()
	if shard.active == nil {
		shard.active = make(map[uint64]struct{})
	}
	shard.active[sequence] = struct{}{}
	shard.mu.Unlock()
	return sequence, nil
}

func (t *rpcAdmissionTracker) retire(sequence uint64) {
	if t == nil || sequence == 0 {
		return
	}
	shard := &t.shards[sequence&(rpcAdmissionTrackerShards-1)]
	shard.mu.Lock()
	if _, ok := shard.active[sequence]; !ok {
		shard.mu.Unlock()
		panic("mtprotoedge: rpc admission sequence retired more than once")
	}
	delete(shard.active, sequence)
	shard.mu.Unlock()
}

// stableSafeFloor returns the lowest sequence which can still publish, or one
// past the last allocated sequence when no owner remains. The short exclusive
// barrier blocks only admission sequence allocation/registration; handler,
// encoding and delivery stay fully concurrent.
func (t *rpcAdmissionTracker) stableSafeFloor(next *atomic.Uint64) uint64 {
	if t == nil || next == nil {
		return 0
	}
	t.allocationBarrier.Lock()
	defer t.allocationBarrier.Unlock()
	var minimum uint64
	for index := range t.shards {
		shard := &t.shards[index]
		shard.mu.Lock()
		for sequence := range shard.active {
			if minimum == 0 || sequence < minimum {
				minimum = sequence
			}
		}
		shard.mu.Unlock()
	}
	if minimum != 0 {
		return minimum
	}
	last := next.Load()
	if last == math.MaxUint64 {
		return math.MaxUint64
	}
	return last + 1
}
