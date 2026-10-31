package store

import (
	"context"
	"errors"

	"telesrv/internal/domain"
)

// ErrTempAuthKeyAlreadyBound means the same temporary key is already bound to another permanent
// key. A temp key's canonical identity must not drift after the first bind; only replaying the
// same binding may idempotently succeed.
var ErrTempAuthKeyAlreadyBound = errors.New("temporary auth key already bound")

// TempAuthKeyBindingStore persists the temp->perm binding of auth.bindTempAuthKey.
type TempAuthKeyBindingStore interface {
	Save(ctx context.Context, binding domain.TempAuthKeyBinding) error
	// SaveWithState performs the same write and returns the exact Layer tuple
	// committed under the identity/key locks. Callers must use this result rather
	// than issuing a post-commit confirmation read.
	SaveWithState(ctx context.Context, binding domain.TempAuthKeyBinding) (domain.TempAuthKeyBindingResult, error)
	GetByTemp(ctx context.Context, tempAuthKeyID [8]byte) (domain.TempAuthKeyBinding, bool, error)
	// DeleteExpired uses the auth_keys handshake protocol expiry as the sole source of truth,
	// reaping temporary keys older than expiredBefore (unix seconds), at most limit per call,
	// returning the key count. Unbound and bound PFS keys must go through the same path; the
	// binding is cleared via ON DELETE CASCADE.
	DeleteExpired(ctx context.Context, expiredBefore int64, limit int) (int, error)
}
