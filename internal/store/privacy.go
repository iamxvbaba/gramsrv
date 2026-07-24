package store

import (
	"context"

	"telesrv/internal/domain"
)

// PrivacyStore persists account privacy rules by owner user and privacy key.
type PrivacyStore interface {
	GetPrivacyRules(ctx context.Context, ownerUserID int64, key domain.PrivacyKey) (domain.PrivacyRules, bool, error)
	SetPrivacyRules(ctx context.Context, rules domain.PrivacyRules) error
	ListPrivacyRules(ctx context.Context, ownerUserIDs []int64, keys []domain.PrivacyKey) ([]domain.PrivacyRules, error)
}

// PrivacyUpdateStore atomically commits an absolute privacy rule snapshot,
// allocates account pts, appends its durable event, and enqueues online
// dispatch. Implementations return the event with its final pts.
type PrivacyUpdateStore interface {
	SetPrivacyRulesWithUpdate(
		ctx context.Context,
		rules domain.PrivacyRules,
		event domain.UpdateEvent,
		excludeAuthKeyID [8]byte,
		excludeSessionID int64,
	) (domain.UpdateEvent, error)
}
