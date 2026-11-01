package store

import (
	"context"
	"errors"
	"math"
)

var (
	// ErrInvalidAuthKeyProtocolExpiry means a new handshake tried to write the
	// migration-only unknown sentinel or a protocol expiry outside the TL int32 timestamp range.
	ErrInvalidAuthKeyProtocolExpiry = errors.New("invalid auth key protocol expiry")
	// ErrAuthKeyProtocolMetadataConflict means the same cryptographic auth_key_id
	// was rewritten to a different key body or a different permanent/temp type/expiry.
	ErrAuthKeyProtocolMetadataConflict = errors.New("auth key protocol metadata conflict")
	// ErrAuthKeyNotFound prevents client metadata writes from silently succeeding
	// after the protocol key row has already disappeared. auth_keys is the
	// authoritative Layer source; an authorization mirror must never advance on
	// its own when that primary write did not happen.
	ErrAuthKeyNotFound = errors.New("auth key not found")
	// ErrAuthKeyNotPermanent prevents an authorization from landing on a temporary/legacy-unknown key.
	ErrAuthKeyNotPermanent = errors.New("auth key is not permanent")
	// ErrAuthKeyBindingInvalid means the temp/perm reference is missing, has the wrong
	// type, or the binding expiry was not normalized to the handshake-authoritative value.
	ErrAuthKeyBindingInvalid = errors.New("invalid temporary auth key binding")
)

// ValidNewAuthKeyProtocolExpiry only allows a handshake to write permanent(0) or a
// positive temporary expiry within the TL int32 range. -1 can only be written by migration 0086.
func ValidNewAuthKeyProtocolExpiry(expiresAt int) bool {
	return expiresAt >= 0 && int64(expiresAt) <= math.MaxInt32
}

// AuthKeyData is one persisted MTProto auth key record.
//
// It does not depend on td protocol types: the connection layer converts between
// crypto.AuthKey and AuthKeyData at the boundary.
type AuthKeyData struct {
	ID         [8]byte   // auth_key_id (low 64 bits of the key's SHA1)
	Value      [256]byte // 2048-bit auth key
	ServerSalt int64     // initial server salt produced by the key exchange
	CreatedAt  int64     // unix seconds
	// ExpiresAt is the protocol expiry of a temporary/media-temporary auth key (unix seconds).
	// 0 is only allowed for a permanent key; -1 only denotes a historical key whose type
	// migration 0086 cannot prove, and the edge must reject it with -404 and force the client
	// to re-handshake. Key type is a handshake fact and cannot be inferred from whether an
	// authorization exists.
	ExpiresAt int
	Layer     int
	// LayerObservationID globally orders durable explicit Layer observations
	// across sessions, processes and restarts. Zero means legacy/no ordered
	// evidence; it is still a usable inherited default but cannot outrank a
	// positive observation during temp-to-perm identity merge.
	LayerObservationID int64
	DeviceModel        string
	Platform           string
	SystemVersion      string
	APIID              int
	AppVersion         string
	// The user binding is not here: auth_key is a protocol artifact, and the
	// authorization (auth_key<->user + device info) is carried by authorization (P2).
}

type AuthKeyClientInfo struct {
	// Layer is the durable last-known default. The RPC boundary validates it
	// against the generated profile set before use; stores must preserve the
	// exact value and must never clamp a future unsupported Layer.
	Layer         int
	DeviceModel   string
	Platform      string
	SystemVersion string
	APIID         int
	AppVersion    string
}

// AuthKeyBindingKeys is the authoritative pair used to verify one
// auth.bindTempAuthKey proof. Both rows are loaded and activity-touched in one
// store operation.
type AuthKeyBindingKeys struct {
	Temporary      AuthKeyData
	TemporaryFound bool
	Permanent      AuthKeyData
	PermanentFound bool
}

type authKeyDeleteOriginContextKey struct{}

// AuthKeyDeleteOrigin carries wire-level destroy_auth_key origin metadata across
// the AuthKeyStore boundary. It is intentionally optional: stores ignore it, but
// remote CoreExec stores use it to keep the requester writable long enough to
// receive destroy_auth_key_ok while every other Edge/session is fenced.
type AuthKeyDeleteOrigin struct {
	ExceptSessionID int64
}

func WithAuthKeyDeleteOrigin(ctx context.Context, origin AuthKeyDeleteOrigin) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if origin.ExceptSessionID == 0 {
		return ctx
	}
	return context.WithValue(ctx, authKeyDeleteOriginContextKey{}, origin)
}

func AuthKeyDeleteOriginFrom(ctx context.Context) (AuthKeyDeleteOrigin, bool) {
	if ctx == nil {
		return AuthKeyDeleteOrigin{}, false
	}
	origin, ok := ctx.Value(authKeyDeleteOriginContextKey{}).(AuthKeyDeleteOrigin)
	return origin, ok && origin.ExceptSessionID != 0
}

// MergeAuthKeyLayerObservations resolves the inherited default when a raw
// temporary key is bound to its permanent identity. Positive observation IDs
// are globally ordered durable evidence. Equal positive IDs must describe the
// same Layer; zero is legacy/unordered and therefore defers to the permanent
// identity. Both rows must be written to the returned tuple atomically.
func MergeAuthKeyLayerObservations(
	tempLayer int,
	tempObservationID int64,
	permLayer int,
	permObservationID int64,
) (layer int, observationID int64, err error) {
	if tempLayer < 0 || permLayer < 0 || tempObservationID < 0 || permObservationID < 0 ||
		(tempObservationID > 0 && tempLayer == 0) ||
		(permObservationID > 0 && permLayer == 0) {
		return 0, 0, ErrAuthKeySessionLayerInvalid
	}
	switch {
	case tempObservationID > permObservationID:
		return tempLayer, tempObservationID, nil
	case permObservationID > tempObservationID:
		return permLayer, permObservationID, nil
	case tempObservationID > 0 && tempLayer != permLayer:
		return 0, 0, ErrAuthKeySessionLayerConflict
	case tempObservationID > 0:
		return tempLayer, tempObservationID, nil
	default:
		return permLayer, 0, nil
	}
}

// AuthKeyStore persists auth keys. See store/memory (test double) and store/postgres for implementations.
type AuthKeyStore interface {
	// Save stores one auth key record; a retry with the same ID may only keep the key body and protocol type/expiry unchanged.
	Save(ctx context.Context, k AuthKeyData) error
	// Get looks up by auth_key_id; found=false when it does not exist.
	Get(ctx context.Context, id [8]byte) (data AuthKeyData, found bool, err error)
	Revalidate(ctx context.Context, id [8]byte) (data AuthKeyData, found bool, err error)
	LoadBindingKeys(ctx context.Context, tempID, permID [8]byte) (AuthKeyBindingKeys, error)
	// UpdateClientInfo merges updated client negotiation metadata into the auth key.
	// When the target key is absent it must return ErrAuthKeyNotFound; a missing primary
	// must never be treated as success and the mirror must not then be updated. Empty
	// fields do not overwrite existing values, and layer/api_id do not overwrite when 0.
	UpdateClientInfo(ctx context.Context, id [8]byte, info AuthKeyClientInfo) error
	// Delete removes an auth key record (destroy_auth_key); a missing record succeeds silently.
	// The connection layer checks this interface by auth_key_id on every frame, so inbound frames for
	// the key become invalid as soon as it is deleted.
	Delete(ctx context.Context, id [8]byte) error
}
