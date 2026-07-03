package store

import (
	"context"

	"telesrv/internal/domain"
)

// AIComposeStore 持久化用户自定义 AI tone 与每用户已保存列表。
// 默认 tone 是代码目录配置，不进 store；memory 与 postgres 行为必须一致。
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
