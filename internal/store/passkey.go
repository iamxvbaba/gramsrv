package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// PasskeyStore persists registered passkey credentials (durable).
type PasskeyStore interface {
	InsertPasskey(ctx context.Context, cred domain.PasskeyCredential) error
	GetPasskeyByCredentialID(ctx context.Context, credentialID []byte) (domain.PasskeyCredential, bool, error)
	ListPasskeysByUser(ctx context.Context, userID int64) ([]domain.PasskeyCredential, error)
	UpdatePasskeyUsage(ctx context.Context, credentialID []byte, signCount uint32, lastUsedAt int64) error
	DeletePasskey(ctx context.Context, userID int64, credentialID []byte) (bool, error)
}

// PasskeyChallengeStore temporarily stores one-time WebAuthn challenges (short TTL, burned after use).
type PasskeyChallengeStore interface {
	SavePasskeyChallenge(ctx context.Context, challenge []byte, c domain.PasskeyChallenge, ttl time.Duration) error
	// ConsumePasskeyChallenge takes out and deletes the challenge; returns found=false when expired or absent.
	ConsumePasskeyChallenge(ctx context.Context, challenge []byte) (domain.PasskeyChallenge, bool, error)
}
