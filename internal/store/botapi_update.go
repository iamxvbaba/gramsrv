package store

import (
	"context"

	"telesrv/internal/domain"
)

// BotAPIUpdateStore persists update_id based Bot API delivery queues.
type BotAPIUpdateStore interface {
	EnqueueBotAPIUpdate(ctx context.Context, req domain.EnqueueBotAPIUpdateRequest) (domain.BotAPIUpdate, bool, error)
	ListBotAPIUpdates(ctx context.Context, botUserID, fromUpdateID int64, limit int) ([]domain.BotAPIUpdate, error)
	ConfirmBotAPIUpdates(ctx context.Context, botUserID, confirmedUpdateID int64) error
	ConfirmedBotAPIUpdateID(ctx context.Context, botUserID int64) (int64, bool, error)
}
