package redisstore

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const botWidgetQueryAnswerChannel = "telesrv:bot_widget_query:answers"

// WidgetQueryStore coordinates bot web-widget queries across processes on top of
// the shared answerQueryStore.
type WidgetQueryStore struct {
	answerQueryStore[domain.BotWidgetQueryAnswer]
}

func NewWidgetQueryStore(c redis.UniversalClient) *WidgetQueryStore {
	return &WidgetQueryStore{answerQueryStore: answerQueryStore[domain.BotWidgetQueryAnswer]{
		c: c, prefix: "telesrv:bot_widget_query", channel: botWidgetQueryAnswerChannel, label: "bot widget query",
	}}
}

func (s *WidgetQueryStore) PutBotWidgetQueryPending(ctx context.Context, pending store.BotWidgetQueryPending, ttl time.Duration) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("invalid bot widget query pending")
	}
	return s.answerQueryStore.putPending(ctx, pending.QueryID, pending.BotUserID, pending.UserID, pending.CreatedAt, ttl)
}

func (s *WidgetQueryStore) ResolveBotWidgetQuery(ctx context.Context, botUserID, queryID int64, answer domain.BotWidgetQueryAnswer) (bool, error) {
	if s == nil {
		return false, nil
	}
	return s.answerQueryStore.resolve(ctx, botUserID, queryID, answer)
}

func (s *WidgetQueryStore) GetBotWidgetQueryAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotWidgetQueryAnswer, bool, error) {
	if s == nil {
		return domain.BotWidgetQueryAnswer{}, false, nil
	}
	return s.answerQueryStore.answer(ctx, botUserID, queryID)
}

func (s *WidgetQueryStore) DeleteBotWidgetQueryPending(ctx context.Context, botUserID, queryID int64) error {
	if s == nil {
		return nil
	}
	return s.answerQueryStore.deletePending(ctx, botUserID, queryID)
}

func (s *WidgetQueryStore) SubscribeBotWidgetQueryAnswers(ctx context.Context, handle func(context.Context, store.BotWidgetQueryAnswerPush)) error {
	if s == nil {
		return nil
	}
	return s.answerQueryStore.subscribe(ctx, func(ctx context.Context, push answerPush[domain.BotWidgetQueryAnswer]) {
		handle(ctx, store.BotWidgetQueryAnswerPush{QueryID: push.QueryID, BotUserID: push.BotUserID, Answer: push.Answer})
	})
}
