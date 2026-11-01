package store

import (
	"context"
	"errors"
	"fmt"

	"telesrv/internal/domain"
)

var ErrAuthorizationStateChanged = errors.New("authorization state changed")

// AuthorizationStore persists device authorizations (auth_key <-> user bindings). See
// store/memory (test double) and store/postgres for implementations.
type AuthorizationStore interface {
	Bind(ctx context.Context, a domain.Authorization) error
	// BindWithDelivery is the successful human-login boundary. The authorization
	// baseline and its exactly-once, non-PTS new-login notification are committed
	// together. A replay of an already fully-authorized auth key is a no-op and
	// must not append another delivery row.
	BindWithDelivery(ctx context.Context, a domain.Authorization, user domain.User, effects DeliveryEffectsBuilder[AuthorizationDeliverySnapshot]) error
	ByAuthKey(ctx context.Context, authKeyID [8]byte) (domain.Authorization, bool, error)
	// UpdateClientInfo merges updated client metadata into the bound authorization so the device list matches the auth key negotiation facts.
	UpdateClientInfo(ctx context.Context, authKeyID [8]byte, info domain.AuthKeyClientInfo) error
	ListByUser(ctx context.Context, userID int64) ([]domain.Authorization, error)
	Delete(ctx context.Context, authKeyID [8]byte) error
	DeleteByHash(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error)
	DeleteByUserExcept(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error)
	// MarkPasswordPassedWithDelivery atomically promotes password_pending and
	// appends the new-login notification. Same-user replay after promotion is an
	// idempotent no-op; cross-user or missing state still fails closed.
	MarkPasswordPassedWithDelivery(ctx context.Context, authKeyID [8]byte, expectedUser domain.User, effects DeliveryEffectsBuilder[AuthorizationDeliverySnapshot]) error
}

// AuthorizationDeliverySnapshot is the immutable post-transition input to the
// RPC-owned TL projector. Store code never imports tg types; the encoded bytes
// return as a DeliveryEffect before the transaction may commit.
type AuthorizationDeliverySnapshot struct {
	Authorization domain.Authorization
	User          domain.User
}

// ValidateAuthorizationDeliveryEffects keeps the successful-login contract
// intentionally narrow: one notification row for the account, excluding the
// newly-authorized physical session. Broad or empty effect sets would either
// duplicate the alert or notify the session that just completed the login.
func ValidateAuthorizationDeliveryEffects(userID int64, effects []DeliveryEffect) error {
	if len(effects) != 1 {
		return fmt.Errorf("authorization delivery requires exactly one effect, got %d", len(effects))
	}
	effect := effects[0]
	if err := effect.Validate(); err != nil {
		return err
	}
	if effect.Kind != DeliveryEffectAbsolute || effect.TargetUserID != userID {
		return fmt.Errorf("authorization delivery must be one absolute effect for user %d", userID)
	}
	if effect.ExcludeAuthKeyID == ([8]byte{}) || effect.ExcludeSessionID == 0 {
		return fmt.Errorf("authorization delivery must exclude the newly-authorized session")
	}
	return nil
}

// AuthKeyAuthorityLinker is an optional in-process store-composition boundary.
// Implementations whose auth_keys primary and authorization projection live in
// separate in-memory objects can attach them here so Layer writes update both
// under one state transition. Durable stores normally enforce this inside one
// database transaction and do not implement the hook.
type AuthKeyAuthorityLinker interface {
	LinkAuthKeyAuthority(AuthKeyStore)
}
