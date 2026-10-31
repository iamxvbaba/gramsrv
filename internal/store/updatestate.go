package store

import (
	"context"

	"telesrv/internal/domain"
)

// UpdateStateStore persists pts/qts/seq state at the auth_key + user dimension, preventing state bleed when the same device switches accounts.
type UpdateStateStore interface {
	Get(ctx context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, bool, error)
	Save(ctx context.Context, authKeyID [8]byte, userID int64, state domain.UpdateState) error
	// CommitDeliveredState atomically advances the cursor proven by one physically
	// delivered response. Baseline mode advances confirmed and observed together;
	// delivered-only mode must leave observed untouched.
	CommitDeliveredState(ctx context.Context, authKeyID [8]byte, userID int64, state domain.UpdateState, mode domain.UpdateStateCommitMode) error
	// ObserveClientState advances only the state that the client has proved it already owns by
	// carrying it in a request. An audited getState baseline advances the same watermark only via
	// the atomic CommitDeliveredState baseline mode after physical delivery. Durable-log retention
	// must use this watermark, never a response state merely sent by server.
	ObserveClientState(ctx context.Context, authKeyID [8]byte, userID int64, state domain.UpdateState) error
	Delete(ctx context.Context, authKeyID [8]byte, userID int64) error
	DeleteAuthKey(ctx context.Context, authKeyID [8]byte) error
}
