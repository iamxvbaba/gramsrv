package phone

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"

	"telesrv/internal/domain"
)

// registry is the in-process authoritative store for active calls: a single implementation, not part of the store dual-implementation scheme.
//
// Rationale (design confirmed, do not revert to dual stores): an active call is
// second-scale transient state, and after a server restart the client-side media connection
// has long been torn down, so "recovering a half-finished call after restart" is not a
// valid requirement; a single implementation lets tests and production share the same code,
// eliminating memory/postgres behavior drift by construction. For multi-instancing, swap in
// a Redis implementation behind the same narrow interface with zero changes to the signaling
// layer.
type registry struct {
	mu       sync.Mutex
	byID     map[int64]*entry
	byRandom map[randomKey]int64 // (callerID, randomID) → callID, absorbing client RPC retries
	active   map[int64]int       // userID → number of non-terminal calls (basis for the concurrency cap)
}

type randomKey struct {
	callerID int64
	randomID int64
}

// entry holds one call; the call field is protected by registry.mu. sigMu separately
// serializes that call's signaling forwarding (enqueueing pushes inside the lock), decoupled
// from the state lock, so state transitions such as discard are not stalled by a peer's
// outbound queue congestion.
type entry struct {
	call domain.PhoneCall

	sigMu        sync.Mutex
	sigWindowSec int64
	sigCount     int
}

func newRegistry() *registry {
	return &registry{
		byID:     make(map[int64]*entry),
		byRandom: make(map[randomKey]int64),
		active:   make(map[int64]int),
	}
}

// sweepTombstonesLocked reclaims only terminal tombstones past their retention (the caller holds r.mu).
//
// A non-terminal call must never be deleted directly by Date in GC: the timeout of
// Requested/Ringing/Accepted must go through Service.ExpireDue for the state transition,
// both-side push, and history persistence; Confirmed has no server-side duration limit and
// must remain addressable for signaling/discard until explicitly hung up or the process
// restarts.
func (r *registry) sweepTombstonesLocked(nowUnix, tombstoneTTLSec int64) {
	for id, e := range r.byID {
		if e.call.Terminal() && nowUnix-int64(e.call.DiscardedAt) > tombstoneTTLSec {
			r.removeLocked(id, e, false)
		}
	}
}

func (r *registry) removeLocked(id int64, e *entry, wasActive bool) {
	delete(r.byID, id)
	key := randomKey{callerID: e.call.AdminID, randomID: e.call.RandomID}
	// After a terminal state the client may reuse random_id to create a new call; when the
	// old tombstone expires, the idempotency index already pointing to the new call must not
	// be deleted along with it.
	if indexedID, ok := r.byRandom[key]; ok && indexedID == id {
		delete(r.byRandom, key)
	}
	if wasActive {
		r.decActiveLocked(e.call.AdminID)
	}
}

func (r *registry) decActiveLocked(userID int64) {
	if n := r.active[userID]; n <= 1 {
		delete(r.active, userID)
	} else {
		r.active[userID] = n - 1
	}
}

// markDiscardedLocked moves a non-terminal entry to a terminal state and updates the concurrency count.
func (r *registry) markDiscardedLocked(e *entry, reason domain.PhoneCallDiscardReason, duration, nowUnix int) {
	r.markDiscardedWithSlugLocked(e, reason, "", duration, nowUnix)
}

func (r *registry) markDiscardedWithSlugLocked(e *entry, reason domain.PhoneCallDiscardReason, reasonSlug string, duration, nowUnix int) {
	if e.call.Terminal() {
		return
	}
	e.call.State = domain.PhoneCallStateDiscarded
	e.call.DiscardReason = reason
	e.call.DiscardReasonSlug = reasonSlug
	e.call.Duration = duration
	e.call.DiscardedAt = nowUnix
	r.decActiveLocked(e.call.AdminID)
}

// newID generates a unique positive int64 within the registry (the caller holds r.mu).
func (r *registry) newIDLocked() (int64, error) {
	for i := 0; i < 32; i++ {
		id, err := randomPositiveInt64()
		if err != nil {
			return 0, err
		}
		if _, exists := r.byID[id]; !exists {
			return id, nil
		}
	}
	return 0, fmt.Errorf("phone: exhausted call id attempts")
}

func randomPositiveInt64() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, fmt.Errorf("phone: random id: %w", err)
	}
	v := int64(binary.BigEndian.Uint64(buf[:]) >> 1)
	if v == 0 {
		v = 1
	}
	return v, nil
}
