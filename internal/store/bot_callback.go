package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// BotCallbackPending is the short-lived, protocol-neutral ownership record for
// one messages.getBotCallbackAnswer request. It is deliberately ephemeral: the
// durable Bot API update remains in BotAPIUpdateStore, while this record only
// coordinates the synchronous client answer across server instances.
type BotCallbackPending struct {
	QueryID   int64
	BotUserID int64
	UserID    int64
	CreatedAt time.Time
}

type BotCallbackAnswerPush struct {
	QueryID   int64
	BotUserID int64
	Answer    domain.BotCallbackAnswer
}

// BotCallbackRegistryStore coordinates callback waiters across processes.
// Implementations must make Put and Resolve atomic: query ids cannot be
// overwritten and at most one answer may win for the owning bot.
type BotCallbackRegistryStore interface {
	PutBotCallbackPending(ctx context.Context, pending BotCallbackPending, ttl time.Duration) (bool, error)
	ResolveBotCallback(ctx context.Context, botUserID, queryID int64, answer domain.BotCallbackAnswer) (bool, error)
	GetBotCallbackAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotCallbackAnswer, bool, error)
	DeleteBotCallbackPending(ctx context.Context, botUserID, queryID int64) error
	SubscribeBotCallbackAnswers(ctx context.Context, handle func(context.Context, BotCallbackAnswerPush)) error
}
