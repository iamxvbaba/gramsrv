package domain

import (
	"errors"
	"time"
)

// NoPaidMessagesTokenTemporaryTTL is the lifetime of a temporary free-messages
// link exported by contacts.exportNoPaidMessagesToken (Layer 232).
const NoPaidMessagesTokenTemporaryTTL = 24 * time.Hour

// ErrNoPaidMessagesTokenInvalid is returned when a free-messages token is
// unknown, revoked, expired or belongs to another user.
var ErrNoPaidMessagesTokenInvalid = errors.New("no paid messages token invalid")

// NoPaidMessagesToken is a free-messages link a user who charges Stars for
// incoming private messages hands out. Redeeming it adds the redeemer to the
// owner's PrivacyKeyNoPaidMessages allow list. A user has at most one active
// permanent and one active temporary token; exporting with revoke replaces it.
type NoPaidMessagesToken struct {
	OwnerUserID int64
	Token       string
	Temporary   bool
	// ExpiresAt is zero for a permanent token.
	ExpiresAt time.Time
	CreatedAt time.Time
}

// ActiveAt reports whether the token can still be redeemed at now.
func (t NoPaidMessagesToken) ActiveAt(now time.Time) bool {
	return t.Token != "" && (t.ExpiresAt.IsZero() || now.Before(t.ExpiresAt))
}
