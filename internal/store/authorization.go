package store

import (
	"context"
	"errors"

	"telesrv/internal/domain"
)

var ErrAuthorizationStateChanged = errors.New("authorization state changed")

// AuthorizationStore persists device authorizations (auth_key <-> user bindings). See
// store/memory (test double) and store/postgres for implementations.
type AuthorizationStore interface {
	Bind(ctx context.Context, a domain.Authorization) error
	ByAuthKey(ctx context.Context, authKeyID [8]byte) (domain.Authorization, bool, error)
	// UpdateClientInfo merges updated client metadata into the bound authorization so the device list matches the auth key negotiation facts.
	UpdateClientInfo(ctx context.Context, authKeyID [8]byte, info domain.AuthKeyClientInfo) error
	ListByUser(ctx context.Context, userID int64) ([]domain.Authorization, error)
	Delete(ctx context.Context, authKeyID [8]byte) error
	DeleteByHash(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error)
	DeleteByUserExcept(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error)
	// MarkPasswordPassed promotes to full authorization only when the auth_key still
	// belongs to expectedUserID and is still password_pending, so an old user's password
	// proof cannot promote a re-bound account.
	MarkPasswordPassed(ctx context.Context, authKeyID [8]byte, expectedUserID int64) error
}

// AuthKeyAuthorityLinker is an optional in-process store-composition boundary.
// Implementations whose auth_keys primary and authorization projection live in
// separate in-memory objects can attach them here so Layer writes update both
// under one state transition. Durable stores normally enforce this inside one
// database transaction and do not implement the hook.
type AuthKeyAuthorityLinker interface {
	LinkAuthKeyAuthority(AuthKeyStore)
}
