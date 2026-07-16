package mtprotoedge

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/iamxvbaba/td/tlprofile"
)

const rpcResultFlightDefaultMaxPending = 8192

var (
	// ErrRPCResultFlightCapacity is returned before installing a new owner when
	// the process-wide in-flight claim table has reached its hard bound.
	ErrRPCResultFlightCapacity     = errors.New("mtproto rpc result in-flight capacity exhausted")
	ErrRPCResultSubscriberCapacity = errors.New("mtproto rpc result subscriber capacity exhausted")
	ErrRPCResultFlightInvalid      = errors.New("mtproto rpc result in-flight claim is invalid")
	ErrRPCResultIdentityMismatch   = errors.New("mtproto rpc result request identity mismatch")
	ErrRPCAdmissionSeqExhausted    = errors.New("mtproto rpc admission sequence exhausted")
)

// rpcResultIdentityMismatchError carries the winner's immutable admission
// profile from inside the cache shard critical section. A replacement Conn can
// re-decode the same naked body under that grammar even if the winner aborts
// immediately after the mismatch is returned.
type rpcResultIdentityMismatchError struct {
	profile    tlprofile.Profile
	hasProfile bool
}

func (e *rpcResultIdentityMismatchError) Error() string { return ErrRPCResultIdentityMismatch.Error() }
func (e *rpcResultIdentityMismatchError) Is(target error) bool {
	return target == ErrRPCResultIdentityMismatch
}

func identityMismatch(identity rpcResultRequestIdentity) error {
	return &rpcResultIdentityMismatchError{profile: identity.profile, hasProfile: identity.valid && identity.profile != 0}
}

type rpcResultRequestIdentity struct {
	exact tlprofile.PreparedIdentity
	// profile is retained separately because PreparedIdentity is opaque.
	// It lets a same-msg_id replay be re-admitted with the original request
	// grammar after the session default has moved to another Layer.
	profile tlprofile.Profile
	valid   bool
}

func (i rpcResultRequestIdentity) matches(requested rpcResultRequestIdentity) bool {
	if !requested.valid {
		// Legacy service/test callers carry no API request identity and preserve
		// the historical cache lookup behavior. Exact callers must always match.
		return true
	}
	return i.valid && i.exact == requested.exact
}

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
	state          rpcResultAcquireState
	admissionSeq   uint64
	encoded        *encodedOutboundMessage
	waiter         *rpcResultWaiter
	owner          *rpcResultOwnerLease
	executionKnown bool
	executionOK    bool
}

// rpcResultFlight is not part of the completed cache TTL lifecycle. Its
// done channel is closed exactly once while holding the owning cache shard lock;
// channel close publishes encoded/ok to all waiters without a waiter goroutine.
type rpcResultFlight struct {
	done                 chan struct{}
	encoded              *encodedOutboundMessage
	ok                   bool
	subscribers          []func(*encodedOutboundMessage, bool)
	executionDone        bool
	executionOK          bool
	executionSubscribers []func(bool)
	// subscriberSlots counts callbacks retained by this pending flight. Result
	// and execution callbacks are charged independently; a replay alias installs
	// both atomically so a capacity failure cannot leave half an alias behind.
	subscriberSlots int
	identity        rpcResultRequestIdentity
	admissionSeq    uint64
	// reservation owns one entry and at least one byte at global, raw-auth and
	// session scopes. Put transfers it to a result/tombstone; Abort releases it.
	reservation *rpcResultBudgetReservation
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
	if fn == nil {
		return ErrRPCResultFlightInvalid
	}
	return w.subscribe(fn, nil)
}

// SubscribeExecution registers a non-blocking callback for the terminal
// business outcome. This is deliberately separate from physical rpc_result
// delivery: invokeAfter* ordering depends on handler completion and must not
// occupy a shared RPC worker while a socket write is pending.
func (w *rpcResultWaiter) SubscribeExecution(fn func(bool)) error {
	if fn == nil {
		return ErrRPCResultFlightInvalid
	}
	return w.subscribe(nil, fn)
}

// SubscribeResultAndExecution atomically installs both halves of one pending
// replay alias. Either both callbacks are retained, or neither is; this avoids
// an execution-only closure surviving when the result subscriber hits a hard
// capacity limit.
func (w *rpcResultWaiter) SubscribeResultAndExecution(
	resultFn func(*encodedOutboundMessage, bool),
	executionFn func(bool),
) error {
	if resultFn == nil || executionFn == nil {
		return ErrRPCResultFlightInvalid
	}
	return w.subscribe(resultFn, executionFn)
}

func (w *rpcResultWaiter) subscribe(
	resultFn func(*encodedOutboundMessage, bool),
	executionFn func(bool),
) error {
	if w == nil || w.cache == nil || w.flight == nil || (resultFn == nil && executionFn == nil) {
		return ErrRPCResultFlightInvalid
	}
	s := w.cache.shard(w.key)
	var (
		encoded        *encodedOutboundMessage
		resultOK       bool
		resultReady    = resultFn == nil
		executionOK    bool
		executionReady = executionFn == nil
	)
	s.mu.Lock()
	if flight, exists := s.pending[w.key]; exists && flight == w.flight {
		slots := 0
		if resultFn != nil {
			slots++
		}
		if executionFn != nil && !flight.executionDone {
			slots++
		}
		if slots > 0 {
			if flight.subscriberSlots > w.cache.subscriberPerFlight-slots ||
				!w.cache.subscriberBudget.reserve(w.key, slots) {
				s.mu.Unlock()
				return ErrRPCResultSubscriberCapacity
			}
			flight.subscriberSlots += slots
		}
		if resultFn != nil {
			flight.subscribers = append(flight.subscribers, resultFn)
		}
		if executionFn != nil {
			if flight.executionDone {
				executionOK, executionReady = flight.executionOK, true
			} else {
				flight.executionSubscribers = append(flight.executionSubscribers, executionFn)
			}
		}
		s.mu.Unlock()
		if executionReady && executionFn != nil {
			executionFn(executionOK)
		}
		return nil
	}
	if resultFn != nil {
		select {
		case <-w.flight.done:
			encoded, resultOK, resultReady = w.flight.encoded, w.flight.ok, true
		default:
		}
	}
	if executionFn != nil && w.flight.executionDone {
		executionOK, executionReady = w.flight.executionOK, true
	}
	s.mu.Unlock()
	// Atomic late subscription also means no callback runs unless every requested
	// terminal state is available.
	if !resultReady || !executionReady {
		return ErrRPCResultFlightInvalid
	}
	if executionFn != nil {
		executionFn(executionOK)
	}
	if resultFn != nil {
		resultFn(encoded, resultOK)
	}
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

// CompleteExecution publishes the handler outcome exactly once while this
// lease still owns the flight. success=false includes RPC errors, internal
// failures and dependency failures. Delivery/cache completion remains a
// separate later transition.
func (l *rpcResultOwnerLease) CompleteExecution(success bool) bool {
	if l == nil || l.cache == nil || l.flight == nil {
		return false
	}
	s := l.cache.shard(l.key)
	s.mu.Lock()
	flight, ok := s.pending[l.key]
	if !ok || flight != l.flight || flight.executionDone {
		s.mu.Unlock()
		return false
	}
	flight.executionDone = true
	flight.executionOK = success
	subscribers := append([]func(bool){}, flight.executionSubscribers...)
	flight.executionSubscribers = nil
	l.cache.releaseFlightSubscriberSlotsLocked(l.key, flight, len(subscribers))
	s.mu.Unlock()
	for _, subscriber := range subscribers {
		subscriber(success)
	}
	return true
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
	if flight.reservation != nil {
		flight.reservation.release()
		flight.reservation = nil
	}
	l.cache.flightLimit.release()
	l.cache.activeAdmissions.retire(flight.admissionSeq)
	subscribers := append([]func(*encodedOutboundMessage, bool){}, flight.subscribers...)
	flight.subscribers = nil
	executionSubscribers := append([]func(bool){}, flight.executionSubscribers...)
	flight.executionSubscribers = nil
	l.cache.releaseFlightSubscriberSlotsLocked(
		l.key, flight, len(subscribers)+len(executionSubscribers),
	)
	if !flight.executionDone {
		flight.executionDone = true
		flight.executionOK = false
	}
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
	for _, subscriber := range executionSubscribers {
		subscriber(false)
	}
	return true
}

type rpcResultFlightLimit struct {
	max  int64
	used atomic.Int64
}

func (l *rpcResultFlightLimit) reserve() bool {
	return l.reserveN(1)
}

func (l *rpcResultFlightLimit) reserveN(delta int64) bool {
	if l == nil || l.max <= 0 {
		return false
	}
	if delta <= 0 {
		return false
	}
	for {
		used := l.used.Load()
		if used < 0 || used > l.max-delta {
			return false
		}
		if l.used.CompareAndSwap(used, used+delta) {
			return true
		}
	}
}

func (l *rpcResultFlightLimit) release() {
	l.releaseN(1)
}

func (l *rpcResultFlightLimit) releaseN(delta int64) {
	if l == nil {
		return
	}
	if delta <= 0 {
		panic("mtproto rpc result counter release must be positive")
	}
	if remaining := l.used.Add(-delta); remaining < 0 {
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
// lifecycle from completed cache TTL but reserve the same global/auth/session
// ownership that Put later transfers to a completed result or tombstone.
func (c *rpcResultCache) Acquire(authKeyID [8]byte, sessionID, reqMsgID int64) (rpcResultAcquire, error) {
	return c.acquire(authKeyID, sessionID, reqMsgID, rpcResultRequestIdentity{})
}

func (c *rpcResultCache) AcquireIdentified(
	authKeyID [8]byte,
	sessionID, reqMsgID int64,
	identity tlprofile.PreparedIdentity,
) (rpcResultAcquire, error) {
	return c.acquire(authKeyID, sessionID, reqMsgID, rpcResultRequestIdentity{exact: identity, valid: true})
}

// AcquireLayerIdentified is the production exact-RPC claim. In addition to the
// immutable full request identity it retains the admission profile required to
// decode a later same-msg_id naked replay under its original grammar.
func (c *rpcResultCache) AcquireLayerIdentified(
	authKeyID [8]byte,
	sessionID, reqMsgID int64,
	profile tlprofile.Profile,
	identity tlprofile.PreparedIdentity,
) (rpcResultAcquire, error) {
	return c.acquire(authKeyID, sessionID, reqMsgID, rpcResultRequestIdentity{
		exact: identity, profile: profile, valid: true,
	})
}

// ExactAdmissionProfile returns the immutable profile of an existing exact
// owner/result. It does not create or join a flight. Callers still perform
// AcquireLayerIdentified after decode, which atomically rejects a same-msg_id
// body change by comparing the full prepared identity.
func (c *rpcResultCache) ExactAdmissionProfile(authKeyID [8]byte, sessionID, reqMsgID int64) (tlprofile.Profile, bool) {
	if c == nil || reqMsgID == 0 {
		return 0, false
	}
	key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: sessionID, reqMsgID: reqMsgID}
	s := c.shard(key)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if elem := s.byKey[key]; elem != nil {
		entry := elem.Value.(*rpcResultCacheEntry)
		if entry.expiresAt.After(now) {
			if entry.identity.valid && entry.identity.profile != 0 {
				return entry.identity.profile, true
			}
			return 0, false
		}
		s.removeElement(elem)
	}
	if flight := s.pending[key]; flight != nil && flight.identity.valid && flight.identity.profile != 0 {
		return flight.identity.profile, true
	}
	return 0, false
}

func (c *rpcResultCache) acquire(
	authKeyID [8]byte,
	sessionID, reqMsgID int64,
	identity rpcResultRequestIdentity,
) (rpcResultAcquire, error) {
	if c == nil || reqMsgID == 0 {
		return rpcResultAcquire{}, ErrRPCResultFlightInvalid
	}
	key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: sessionID, reqMsgID: reqMsgID}
	s := c.shard(key)
	reclaimedExpired := false
	for {
		now := s.now()
		s.mu.Lock()
		s.expireLocked(now)

		if elem, ok := s.byKey[key]; ok {
			entry := elem.Value.(*rpcResultCacheEntry)
			if !entry.identity.matches(identity) {
				s.mu.Unlock()
				return rpcResultAcquire{}, identityMismatch(entry.identity)
			}
			if entry.capacity || entry.encoded == nil {
				s.mu.Unlock()
				return rpcResultAcquire{}, ErrRPCResultFlightCapacity
			}
			result := rpcResultAcquire{
				state: rpcResultAcquireCompleted, admissionSeq: entry.admissionSeq, encoded: entry.encoded,
				executionKnown: entry.executionKnown, executionOK: entry.executionOK,
			}
			s.mu.Unlock()
			return result, nil
		}
		if flight, ok := s.pending[key]; ok {
			if !flight.identity.matches(identity) {
				s.mu.Unlock()
				return rpcResultAcquire{}, identityMismatch(flight.identity)
			}
			result := rpcResultAcquire{
				state:        rpcResultAcquirePending,
				admissionSeq: flight.admissionSeq,
				waiter:       &rpcResultWaiter{cache: c, key: key, flight: flight},
			}
			s.mu.Unlock()
			return result, nil
		}
		if s.maxEntries > 0 && len(s.byKey)+len(s.pending) >= s.maxEntries {
			s.mu.Unlock()
			return rpcResultAcquire{}, ErrRPCResultFlightCapacity
		}
		if !c.flightLimit.reserve() {
			s.mu.Unlock()
			return rpcResultAcquire{}, ErrRPCResultFlightCapacity
		}
		reservation := c.fairBudget.reserveOwner(key)
		if reservation == nil {
			c.flightLimit.release()
			s.mu.Unlock()
			if reclaimedExpired {
				return rpcResultAcquire{}, ErrRPCResultFlightCapacity
			}
			// Expired rows in another full-key shard may be the only consumers at
			// the global, auth or session scope. Reap once, then retry every identity
			// and capacity check because another goroutine may have won this key.
			c.expireCompletedResults()
			reclaimedExpired = true
			continue
		}
		var admissionSeq uint64
		if identity.valid {
			var err error
			admissionSeq, err = c.activeAdmissions.allocateAndRegister(&c.nextAdmissionSeq)
			if err != nil {
				reservation.release()
				c.flightLimit.release()
				s.mu.Unlock()
				return rpcResultAcquire{}, err
			}
		}
		flight := &rpcResultFlight{
			done: make(chan struct{}), identity: identity, admissionSeq: admissionSeq,
			reservation: reservation,
		}
		if s.pending == nil {
			s.pending = make(map[rpcResultCacheKey]*rpcResultFlight)
		}
		s.pending[key] = flight
		s.mu.Unlock()
		return rpcResultAcquire{
			state:        rpcResultAcquireOwner,
			admissionSeq: admissionSeq,
			owner: &rpcResultOwnerLease{
				cache: c, key: key, flight: flight, delivery: newRPCResultDelivery(reqMsgID),
			},
		}, nil
	}
}

// completeRPCResultFlightLocked publishes encoded to the current owner claim.
// The caller must hold s.mu and must publish the completed cache entry first.
func (c *rpcResultCache) completeRPCResultFlightLocked(
	s *rpcResultCacheShard,
	key rpcResultCacheKey,
	encoded *encodedOutboundMessage,
) (
	[]func(*encodedOutboundMessage, bool),
	[]func(bool),
	bool,
) {
	if c == nil || s == nil || encoded == nil {
		return nil, nil, false
	}
	flight, ok := s.pending[key]
	if !ok {
		return nil, nil, false
	}
	delete(s.pending, key)
	if flight.reservation != nil {
		flight.reservation.releasePending()
		// The completed entry now owns the same reservation.
		flight.reservation = nil
	}
	flight.encoded = encoded
	flight.ok = true
	c.flightLimit.release()
	c.activeAdmissions.retire(flight.admissionSeq)
	subscribers := append([]func(*encodedOutboundMessage, bool){}, flight.subscribers...)
	flight.subscribers = nil
	executionSubscribers := append([]func(bool){}, flight.executionSubscribers...)
	flight.executionSubscribers = nil
	executionOK := flight.executionOK
	if !flight.executionDone {
		// A result without an explicit handler-completion proof cannot satisfy an
		// invokeAfter dependency. Production completes execution before Put; this
		// branch is the conservative terminal cleanup for defensive callers.
		flight.executionDone = true
		flight.executionOK = false
		executionOK = false
	}
	c.releaseFlightSubscriberSlotsLocked(
		key, flight, len(subscribers)+len(executionSubscribers),
	)
	close(flight.done)
	return subscribers, executionSubscribers, executionOK
}

// releaseFlightSubscriberSlotsLocked releases callbacks detached from a flight.
// The caller holds that flight's cache shard lock, preserving the only lock
// order used by subscription: shard -> subscriber budget.
func (c *rpcResultCache) releaseFlightSubscriberSlotsLocked(
	key rpcResultCacheKey,
	flight *rpcResultFlight,
	slots int,
) {
	if slots == 0 {
		return
	}
	if c == nil || flight == nil || slots < 0 || flight.subscriberSlots < slots {
		panic("mtproto rpc result subscriber slot underflow")
	}
	flight.subscriberSlots -= slots
	c.subscriberBudget.release(key, slots)
}
