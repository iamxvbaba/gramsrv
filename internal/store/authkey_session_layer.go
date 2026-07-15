package store

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrAuthKeySessionLayerInvalid rejects malformed protocol evidence before it
	// can enter the durable same-session ordering boundary.
	ErrAuthKeySessionLayerInvalid = errors.New("invalid auth key session layer evidence")
	// ErrAuthKeySessionLayerConflict means one client msg_id selected two
	// different Layers for the same raw auth key and logical MTProto session.
	ErrAuthKeySessionLayerConflict = errors.New("conflicting auth key session layer evidence")
)

// AuthKeySessionLayer is the short-lived durable high-water mark for explicit
// invokeWithLayer evidence on one logical MTProto session. It is deliberately
// keyed by the raw (wire) auth key rather than the canonical permanent key:
// two PFS temporary keys may reuse a session_id without becoming one session.
//
// ExpiresAt bounds storage and is not a protocol permission. Once this proof
// expires, a new/naked session uses the auth-key-wide last-known Layer default;
// old client msg_ids outside the MTProto freshness window remain request-local
// and cannot recreate shared state.
type AuthKeySessionLayer struct {
	Layer         int
	MessageID     int64
	ObservationID int64
	ExpiresAt     time.Time
	// SharedDefault is populated by AdvanceSessionLayer. It is true when this
	// session observation is still the current auth-key-wide default after the
	// transaction. A duplicate may safely refresh local caches only in that
	// case; an older session must not overwrite a newer session's default.
	SharedDefault bool
}

// AuthKeySessionLayerStore linearizes explicit Layer evidence across server
// processes and restarts. AdvanceSessionLayer never replaces a live row with a
// lower msg_id. applied is true only for an insert, a strictly newer msg_id, or
// replacement of an expired row; duplicate/older evidence returns the current
// row with applied=false. A successful advance and the auth-key-wide default
// update share one store transaction and one globally ordered ObservationID;
// callers must not persist the default in a second best-effort write.
// AdvanceSessionLayer derives ExpiresAt from a fresh client msg_id at the store
// boundary; no caller-controlled retention duration is accepted.
type AuthKeySessionLayerStore interface {
	GetSessionLayer(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64) (value AuthKeySessionLayer, found bool, err error)
	AdvanceSessionLayer(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64, layer int, msgID int64) (current AuthKeySessionLayer, applied bool, err error)
	DeleteSessionLayer(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64) (deleted bool, err error)
	DeleteExpiredSessionLayers(ctx context.Context, limit int) (deleted int, err error)
}

// AuthKeySessionLayerExpiry derives the complete mutable lifetime of explicit
// Layer evidence from the client MTProto msg_id which carried it. The caller
// cannot choose a longer retention window: that would let stale selectors keep
// rewriting the auth-key-wide default after their replay-admission authority
// expired. The calculation intentionally mirrors proto.MessageID.Time without
// importing the protocol package into the store boundary.
func AuthKeySessionLayerExpiry(msgID int64) (time.Time, bool) {
	if msgID <= 0 || msgID%4 != 0 || uint32(msgID) == 0 {
		// MTProto client message ids are 0 modulo 4 and must carry a
		// non-empty lower-32-bit fractional component.
		return time.Time{}, false
	}
	createdAt := time.Unix(msgID>>32, int64(int32(msgID))).UTC()
	return createdAt.Add(301 * time.Second), true
}

// AuthKeySessionLayerEvidenceFresh applies the MTProto freshness envelope at
// the durable write boundary. Edge admission normally rejects stale/future
// messages first; the store repeats the invariant so an alternate caller can
// neither publish an already-expired selector nor manufacture future state.
func AuthKeySessionLayerEvidenceFresh(now time.Time, msgID int64) (time.Time, bool) {
	expiresAt, ok := AuthKeySessionLayerExpiry(msgID)
	if !ok || !now.Before(expiresAt) {
		return time.Time{}, false
	}
	createdAt := expiresAt.Add(-301 * time.Second)
	if createdAt.After(now.Add(30 * time.Second)) {
		return time.Time{}, false
	}
	return expiresAt, true
}
