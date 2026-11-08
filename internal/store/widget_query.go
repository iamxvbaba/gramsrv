package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// BotWidgetQueryPending is the short-lived, protocol-neutral ownership record
// for one messages.getBotWidgetQueryAnswer request. It is deliberately
// ephemeral: it only coordinates the synchronous client answer across server
// instances, mirroring BotCallbackPending.
type BotWidgetQueryPending struct {
	QueryID   int64
	BotUserID int64
	UserID    int64
	CreatedAt time.Time
}

type BotWidgetQueryAnswerPush struct {
	QueryID   int64
	BotUserID int64
	Answer    domain.BotWidgetQueryAnswer
}

// WidgetQueryStore coordinates bot web-widget query waiters across processes.
// Implementations must make Put and Resolve atomic: query ids cannot be
// overwritten and at most one answer may win for the owning bot.
type WidgetQueryStore interface {
	PutBotWidgetQueryPending(ctx context.Context, pending BotWidgetQueryPending, ttl time.Duration) (bool, error)
	ResolveBotWidgetQuery(ctx context.Context, botUserID, queryID int64, answer domain.BotWidgetQueryAnswer) (bool, error)
	GetBotWidgetQueryAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotWidgetQueryAnswer, bool, error)
	DeleteBotWidgetQueryPending(ctx context.Context, botUserID, queryID int64) error
	SubscribeBotWidgetQueryAnswers(ctx context.Context, handle func(context.Context, BotWidgetQueryAnswerPush)) error
}
