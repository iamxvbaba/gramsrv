package store

import (
	"context"

	"telesrv/internal/domain"
)

// UpdateStateStore 持久化 auth_key + user 维度的 pts/qts/seq 状态，避免同一设备换号串状态。
type UpdateStateStore interface {
	Get(ctx context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, bool, error)
	Save(ctx context.Context, authKeyID [8]byte, userID int64, state domain.UpdateState) error
	// ObserveClientState advances only the state that the client has proved it already owns by
	// carrying it in a request (or by explicitly establishing a getState snapshot baseline).
	// Durable-log retention must use this watermark, never a response state merely sent by server.
	ObserveClientState(ctx context.Context, authKeyID [8]byte, userID int64, state domain.UpdateState) error
	Delete(ctx context.Context, authKeyID [8]byte, userID int64) error
	DeleteAuthKey(ctx context.Context, authKeyID [8]byte) error
}
