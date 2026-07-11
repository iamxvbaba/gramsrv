package mtprotoedge

import (
	"context"
	"errors"
	"sync/atomic"
)

const rpcResultFlightDefaultMaxPending = 8192

var (
	// ErrRPCResultFlightCapacity is returned before installing a new owner when
	// the process-wide in-flight claim table has reached its hard bound.
	ErrRPCResultFlightCapacity = errors.New("mtproto rpc result in-flight capacity exhausted")
	ErrRPCResultFlightInvalid  = errors.New("mtproto rpc result in-flight claim is invalid")
)

type rpcResultAcquireState uint8

const (
	rpcResultAcquireCompleted rpcResultAcquireState = iota + 1
	rpcResultAcquirePending
	rpcResultAcquireOwner
)

// rpcResultAcquire is the atomic outcome for one
// (auth_key_id, session_id, req_msg_id) claim.
//
// Exactly one state-specific field is non-nil:
//   - completed: encoded contains the immutable completed rpc_result;
//   - pending: waiter joins the already-running owner;
//   - owner: owner must eventually complete through rpcResultCache.Put or Abort.
type rpcResultAcquire struct {
	state   rpcResultAcquireState
	encoded *encodedOutboundMessage
	waiter  *rpcResultWaiter
	owner   *rpcResultOwnerLease
}

// rpcResultFlight is not part of the completed cache LRU/TTL lifecycle. Its
// done channel is closed exactly once while holding the owning cache shard lock;
// channel close publishes encoded/ok to all waiters without a waiter goroutine.
type rpcResultFlight struct {
	done    chan struct{}
	encoded *encodedOutboundMessage
	ok      bool
}

type rpcResultWaiter struct {
	flight *rpcResultFlight
}

// Wait blocks until the owner publishes through Put, aborts, or ctx expires.
// ok=false with err=nil means the owner aborted without a result.
func (w *rpcResultWaiter) Wait(ctx context.Context) (encoded *encodedOutboundMessage, ok bool, err error) {
	if w == nil || w.flight == nil || ctx == nil {
		return nil, false, ErrRPCResultFlightInvalid
	}

	// Prefer an already-published result over a concurrently canceled context.
	select {
	case <-w.flight.done:
		return w.flight.encoded, w.flight.ok, nil
	default:
	}

	select {
	case <-w.flight.done:
		return w.flight.encoded, w.flight.ok, nil
	case <-ctx.Done():
		// If completion raced with cancellation, prefer the terminal flight state.
		select {
		case <-w.flight.done:
			return w.flight.encoded, w.flight.ok, nil
		default:
			return nil, false, ctx.Err()
		}
	}
}

type rpcResultOwnerLease struct {
	cache  *rpcResultCache
	key    rpcResultCacheKey
	flight *rpcResultFlight
}

// Abort releases an unfinished owner claim and wakes every waiter with no
// result. Pointer identity prevents an old lease from deleting a later owner
// that reacquired the same key. It returns true only for the winning abort.
func (l *rpcResultOwnerLease) Abort() bool {
	if l == nil || l.cache == nil || l.flight == nil {
		return false
	}
	s := l.cache.shard(l.key)
	s.mu.Lock()
	defer s.mu.Unlock()

	flight, ok := s.pending[l.key]
	if !ok || flight != l.flight {
		return false
	}
	delete(s.pending, l.key)
	l.cache.flightLimit.release()
	close(flight.done)
	return true
}

type rpcResultFlightLimit struct {
	max  int64
	used atomic.Int64
}

func (l *rpcResultFlightLimit) reserve() bool {
	if l == nil || l.max <= 0 {
		return false
	}
	for {
		used := l.used.Load()
		if used >= l.max {
			return false
		}
		if l.used.CompareAndSwap(used, used+1) {
			return true
		}
	}
}

func (l *rpcResultFlightLimit) release() {
	if l == nil {
		return
	}
	if remaining := l.used.Add(-1); remaining < 0 {
		// Put/Abort use map removal and lease identity to make double release
		// impossible. Fail fast instead of masking a capacity-accounting bug that
		// could otherwise admit more owners than the configured hard limit.
		panic("mtproto rpc result in-flight counter underflow")
	}
}

func (l *rpcResultFlightLimit) snapshot() int64 {
	if l == nil {
		return 0
	}
	return l.used.Load()
}

// Acquire atomically returns a completed result, joins the existing in-flight
// owner, or installs the unique owner lease. Pending entries have a separate
// lifecycle from completed cache trim/TTL and consume one process-wide slot.
func (c *rpcResultCache) Acquire(authKeyID [8]byte, sessionID, reqMsgID int64) (rpcResultAcquire, error) {
	if c == nil || reqMsgID == 0 {
		return rpcResultAcquire{}, ErrRPCResultFlightInvalid
	}
	key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: sessionID, reqMsgID: reqMsgID}
	s := c.shard(key)
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if elem, ok := s.byKey[key]; ok {
		entry := elem.Value.(*rpcResultCacheEntry)
		if entry.expiresAt.After(now) {
			return rpcResultAcquire{state: rpcResultAcquireCompleted, encoded: entry.encoded}, nil
		}
		s.removeElement(elem)
	}
	if flight, ok := s.pending[key]; ok {
		return rpcResultAcquire{
			state:  rpcResultAcquirePending,
			waiter: &rpcResultWaiter{flight: flight},
		}, nil
	}
	if !c.flightLimit.reserve() {
		return rpcResultAcquire{}, ErrRPCResultFlightCapacity
	}
	flight := &rpcResultFlight{done: make(chan struct{})}
	if s.pending == nil {
		s.pending = make(map[rpcResultCacheKey]*rpcResultFlight)
	}
	s.pending[key] = flight
	return rpcResultAcquire{
		state: rpcResultAcquireOwner,
		owner: &rpcResultOwnerLease{cache: c, key: key, flight: flight},
	}, nil
}

// completeRPCResultFlightLocked publishes encoded to the current owner claim.
// The caller must hold s.mu and must publish the completed cache entry first.
func (c *rpcResultCache) completeRPCResultFlightLocked(
	s *rpcResultCacheShard,
	key rpcResultCacheKey,
	encoded *encodedOutboundMessage,
) {
	if c == nil || s == nil || encoded == nil {
		return
	}
	flight, ok := s.pending[key]
	if !ok {
		return
	}
	delete(s.pending, key)
	flight.encoded = encoded
	flight.ok = true
	c.flightLimit.release()
	close(flight.done)
}
