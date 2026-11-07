package redisstore

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const botCallbackAnswerChannel = "telesrv:bot_callback:answers"

// BotCallbackRegistryStore coordinates callback waiters across processes on top
// of the shared answerQueryStore.
type BotCallbackRegistryStore struct {
	answerQueryStore[domain.BotCallbackAnswer]
}

func NewBotCallbackRegistryStore(c redis.UniversalClient) *BotCallbackRegistryStore {
	return &BotCallbackRegistryStore{answerQueryStore: answerQueryStore[domain.BotCallbackAnswer]{
		c: c, prefix: "telesrv:bot_callback", channel: botCallbackAnswerChannel, label: "bot callback",
	}}
}

func (s *BotCallbackRegistryStore) PutBotCallbackPending(ctx context.Context, pending store.BotCallbackPending, ttl time.Duration) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("invalid bot callback pending")
	}
	return s.answerQueryStore.putPending(ctx, pending.QueryID, pending.BotUserID, pending.UserID, pending.CreatedAt, ttl)
}

func (s *BotCallbackRegistryStore) ResolveBotCallback(ctx context.Context, botUserID, queryID int64, answer domain.BotCallbackAnswer) (bool, error) {
	if s == nil {
		return false, nil
	}
	return s.answerQueryStore.resolve(ctx, botUserID, queryID, answer)
}

func (s *BotCallbackRegistryStore) GetBotCallbackAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotCallbackAnswer, bool, error) {
	if s == nil {
		return domain.BotCallbackAnswer{}, false, nil
	}
	return s.answerQueryStore.answer(ctx, botUserID, queryID)
}

func (s *BotCallbackRegistryStore) DeleteBotCallbackPending(ctx context.Context, botUserID, queryID int64) error {
	if s == nil {
		return nil
	}
	return s.answerQueryStore.deletePending(ctx, botUserID, queryID)
}

func (s *BotCallbackRegistryStore) SubscribeBotCallbackAnswers(ctx context.Context, handle func(context.Context, store.BotCallbackAnswerPush)) error {
	if s == nil {
		return nil
	}
	return s.answerQueryStore.subscribe(ctx, func(ctx context.Context, push answerPush[domain.BotCallbackAnswer]) {
		handle(ctx, store.BotCallbackAnswerPush{QueryID: push.QueryID, BotUserID: push.BotUserID, Answer: push.Answer})
	})
}
