package mtprotoedge

import (
	"context"
	"errors"
	"sync"
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
	done        chan struct{}
	encoded     *encodedOutboundMessage
	ok          bool
	subscribers []func(*encodedOutboundMessage, bool)
}

type rpcResultWaiter struct {
	cache  *rpcResultCache
	key    rpcResultCacheKey
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

// Subscribe registers an event callback without creating a goroutine or
// occupying an RPC worker. The callback is invoked after the cache shard lock is
// released; it must remain non-blocking.
func (w *rpcResultWaiter) Subscribe(fn func(*encodedOutboundMessage, bool)) error {
	if w == nil || w.cache == nil || w.flight == nil || fn == nil {
		return ErrRPCResultFlightInvalid
	}
	s := w.cache.shard(w.key)
	var (
		encoded *encodedOutboundMessage
		ok      bool
		ready   bool
	)
	s.mu.Lock()
	if flight, exists := s.pending[w.key]; exists && flight == w.flight {
		flight.subscribers = append(flight.subscribers, fn)
		s.mu.Unlock()
		return nil
	}
	select {
	case <-w.flight.done:
		encoded, ok, ready = w.flight.encoded, w.flight.ok, true
	default:
	}
	s.mu.Unlock()
	if !ready {
		return ErrRPCResultFlightInvalid
	}
	fn(encoded, ok)
	return nil
}

type rpcResultOwnerLease struct {
	cache     *rpcResultCache
	key       rpcResultCacheKey
	flight    *rpcResultFlight
	delivery  *rpcResultDelivery
	hookMu    sync.Mutex
	abortHook func()
	// handedOff means the inbound worker transferred terminal-result ownership to
	// the bounded egress pipeline. Its ordinary release callback must no longer
	// abort the flight merely because the socket write is still pending.
	handedOff atomic.Bool
}

func (l *rpcResultOwnerLease) SetAbortHook(fn func()) {
	if l == nil {
		return
	}
	l.hookMu.Lock()
	l.abortHook = fn
	l.hookMu.Unlock()
}

// InstallAbortHook installs fn only while this lease still owns the pending
// flight. The shard lock linearizes installation with Abort/Put so a registry
// cannot publish a candidate after its owner has already disappeared.
func (l *rpcResultOwnerLease) InstallAbortHook(fn func()) bool {
	if l == nil || l.cache == nil || l.flight == nil || fn == nil {
		return false
	}
	s := l.cache.shard(l.key)
	s.mu.Lock()
	flight, ok := s.pending[l.key]
	if !ok || flight != l.flight {
		s.mu.Unlock()
		return false
	}
	l.hookMu.Lock()
	l.abortHook = fn
	l.hookMu.Unlock()
	s.mu.Unlock()
	return true
}

func (l *rpcResultOwnerLease) Waiter() *rpcResultWaiter {
	if l == nil || l.cache == nil || l.flight == nil {
		return nil
	}
	return &rpcResultWaiter{cache: l.cache, key: l.key, flight: l.flight}
}

func (l *rpcResultOwnerLease) TryRetarget(reqMsgID int64) bool {
	return l != nil && l.delivery != nil && (&encodedOutboundMessage{delivery: l.delivery}).tryRetarget(reqMsgID)
}

func (l *rpcResultOwnerLease) Delivery() *rpcResultDelivery {
	if l == nil {
		return nil
	}
	return l.delivery
}

// HandOff transfers completion responsibility from the inbound RPC task to an
// already-admitted egress operation. The egress terminal callback must resolve
// the flight through Put on both successful delivery and fenced failure.
func (l *rpcResultOwnerLease) HandOff() bool {
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
	l.handedOff.Store(true)
	return true
}

// Abort releases an unfinished owner claim and wakes every waiter with no
// result. Pointer identity prevents an old lease from deleting a later owner
// that reacquired the same key. It returns true only for the winning abort.
func (l *rpcResultOwnerLease) Abort() bool {
	if l == nil || l.cache == nil || l.flight == nil {
		return false
	}
	if l.handedOff.Load() {
		return false
	}
	s := l.cache.shard(l.key)
	s.mu.Lock()
	if l.handedOff.Load() {
		s.mu.Unlock()
		return false
	}

	flight, ok := s.pending[l.key]
	if !ok || flight != l.flight {
		s.mu.Unlock()
		return false
	}
	delete(s.pending, l.key)
	l.cache.flightLimit.release()
	subscribers := append([]func(*encodedOutboundMessage, bool){}, flight.subscribers...)
	flight.subscribers = nil
	close(flight.done)
	s.mu.Unlock()
	l.hookMu.Lock()
	abortHook := l.abortHook
	l.abortHook = nil
	l.hookMu.Unlock()
	if abortHook != nil {
		abortHook()
	}
	for _, subscriber := range subscribers {
		subscriber(nil, false)
	}
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
			waiter: &rpcResultWaiter{cache: c, key: key, flight: flight},
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
		owner: &rpcResultOwnerLease{
			cache: c, key: key, flight: flight, delivery: newRPCResultDelivery(reqMsgID),
		},
	}, nil
}

// completeRPCResultFlightLocked publishes encoded to the current owner claim.
// The caller must hold s.mu and must publish the completed cache entry first.
func (c *rpcResultCache) completeRPCResultFlightLocked(
	s *rpcResultCacheShard,
	key rpcResultCacheKey,
	encoded *encodedOutboundMessage,
) []func(*encodedOutboundMessage, bool) {
	if c == nil || s == nil || encoded == nil {
		return nil
	}
	flight, ok := s.pending[key]
	if !ok {
		return nil
	}
	delete(s.pending, key)
	flight.encoded = encoded
	flight.ok = true
	c.flightLimit.release()
	subscribers := append([]func(*encodedOutboundMessage, bool){}, flight.subscribers...)
	flight.subscribers = nil
	close(flight.done)
	return subscribers
}
