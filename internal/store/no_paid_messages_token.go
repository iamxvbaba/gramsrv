package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// NoPaidMessagesTokenStore persists Layer 232 free-messages links.
//
// ExportNoPaidMessagesToken returns the owner's active token of the requested
// kind (permanent or temporary). When none is active, or revoke is set, every
// active token of that kind is revoked and candidate becomes the new token;
// the operation is atomic per owner and kind. candidate must be a fresh,
// unguessable value; it is unused when an active token is returned.
//
// ResolveNoPaidMessagesToken returns the token only when it belongs to owner,
// is not revoked and is still active at now; otherwise found is false.
type NoPaidMessagesTokenStore interface {
	ExportNoPaidMessagesToken(ctx context.Context, ownerUserID int64, temporary, revoke bool, candidate string, now time.Time) (domain.NoPaidMessagesToken, error)
	ResolveNoPaidMessagesToken(ctx context.Context, ownerUserID int64, token string, now time.Time) (domain.NoPaidMessagesToken, bool, error)
}
