package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// BotAPIPollLeaseStore serializes getUpdates across all HTTP gateway
// instances. owner is an opaque per-request token; Release must be compare-and-
// delete so a stale request cannot release a successor's lease.
type BotAPIPollLeaseStore interface {
	AcquireBotAPIPollLease(ctx context.Context, botUserID int64, owner string, ttl time.Duration) (bool, error)
	ReleaseBotAPIPollLease(ctx context.Context, botUserID int64, owner string) error
}

// BotAPIWebhookStore coordinates durable webhook configuration and delivery
// leases. Delivery itself stays at the HTTP edge; this store only owns state.
type BotAPIWebhookStore interface {
	SetBotAPIWebhook(ctx context.Context, config domain.BotAPIWebhook, dropPending bool) error
	DeleteBotAPIWebhook(ctx context.Context, botUserID int64, dropPending bool) error
	BotAPIWebhook(ctx context.Context, botUserID int64) (domain.BotAPIWebhook, bool, error)
	ListDueBotAPIWebhooks(ctx context.Context, limit int) ([]domain.BotAPIWebhook, error)
	AcquireBotAPIWebhookLease(ctx context.Context, botUserID int64, owner string, ttl time.Duration) (bool, error)
	ReleaseBotAPIWebhookLease(ctx context.Context, botUserID int64, owner string) error
	RecordBotAPIWebhookFailure(ctx context.Context, botUserID int64, owner string, nextAttempt time.Time, message string) error
	RecordBotAPIWebhookSuccess(ctx context.Context, botUserID int64, owner string, nextAttempt time.Time) error
}

// BotAPIUpdateStore persists update_id based Bot API delivery queues.
type BotAPIUpdateStore interface {
	EnqueueBotAPIUpdate(ctx context.Context, req domain.EnqueueBotAPIUpdateRequest) (domain.BotAPIUpdate, bool, error)
	ListBotAPIUpdates(ctx context.Context, botUserID, fromUpdateID int64, limit int) ([]domain.BotAPIUpdate, error)
	ListTailBotAPIUpdates(ctx context.Context, botUserID int64, tail, limit int) ([]domain.BotAPIUpdate, error)
	ConfirmBotAPIUpdates(ctx context.Context, botUserID, confirmedUpdateID int64) error
	ConfirmedBotAPIUpdateID(ctx context.Context, botUserID int64) (int64, bool, error)
	SetBotAPIAllowedUpdates(ctx context.Context, botUserID int64, allowed []domain.BotAPIUpdateKind) error
	DropPendingBotAPIUpdates(ctx context.Context, botUserID int64) error
	PendingBotAPIUpdateCount(ctx context.Context, botUserID int64) (int, error)
}
