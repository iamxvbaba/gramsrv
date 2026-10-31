package store

import (
	"context"

	"telesrv/internal/domain"
)

// AIComposeStore persists user-defined AI tones and each user's saved list.
// The default tone is code-directory configuration and does not go through the
// store; memory and postgres must behave identically.
type AIComposeStore interface {
	CreateAIComposeTone(ctx context.Context, tone domain.AIComposeTone) error
	UpdateAIComposeTone(ctx context.Context, tone domain.AIComposeTone) error
	DeleteAIComposeTone(ctx context.Context, ownerUserID, toneID int64) error
	GetAIComposeToneByID(ctx context.Context, id, accessHash int64) (domain.AIComposeTone, bool, error)
	GetAIComposeToneBySlug(ctx context.Context, slug string) (domain.AIComposeTone, bool, error)
	ListAIComposeTonesForUser(ctx context.Context, userID int64) ([]domain.AIComposeTone, error)
	SaveAIComposeTone(ctx context.Context, userID, toneID int64) error
	UnsaveAIComposeTone(ctx context.Context, userID, toneID int64) error
	SavedAIComposeToneCount(ctx context.Context, userID int64) (int, error)
}
