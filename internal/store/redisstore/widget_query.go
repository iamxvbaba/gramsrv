package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const botWidgetQueryAnswerChannel = "telesrv:bot_widget_query:answers"

// WidgetQueryStore coordinates bot web-widget queries across nodes, mirroring
// BotCallbackRegistryStore.
type WidgetQueryStore struct {
	c redis.UniversalClient
}

func NewWidgetQueryStore(c redis.UniversalClient) *WidgetQueryStore {
	return &WidgetQueryStore{c: c}
}

func botWidgetQueryKey(queryID int64) string {
	return fmt.Sprintf("telesrv:bot_widget_query:%d", queryID)
}

var putBotWidgetQueryScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) ~= 0 then
  return 0
end
redis.call('HSET', KEYS[1],
  'bot_user_id', ARGV[1],
  'user_id', ARGV[2],
  'created_at_unix_nano', ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[4])
return 1
`)

func (s *WidgetQueryStore) PutBotWidgetQueryPending(ctx context.Context, pending store.BotWidgetQueryPending, ttl time.Duration) (bool, error) {
	if s == nil || s.c == nil || pending.QueryID == 0 || pending.BotUserID <= 0 || pending.UserID <= 0 || ttl <= 0 {
		return false, fmt.Errorf("invalid bot widget query pending")
	}
	createdAt := pending.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	result, err := putBotWidgetQueryScript.Run(ctx, s.c, []string{botWidgetQueryKey(pending.QueryID)},
		pending.BotUserID, pending.UserID, createdAt.UnixNano(), ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("put bot widget query pending: %w", err)
	}
	return result == 1, nil
}

var resolveBotWidgetQueryScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'bot_user_id') ~= ARGV[1] then
  return 0
end
if redis.call('HEXISTS', KEYS[1], 'answer') ~= 0 then
  return 0
end
redis.call('HSET', KEYS[1], 'answer', ARGV[2])
redis.call('PUBLISH', ARGV[3], ARGV[4])
return 1
`)

func (s *WidgetQueryStore) ResolveBotWidgetQuery(ctx context.Context, botUserID, queryID int64, answer domain.BotWidgetQueryAnswer) (bool, error) {
	if s == nil || s.c == nil || botUserID <= 0 || queryID == 0 {
		return false, nil
	}
	answerJSON, err := json.Marshal(answer)
	if err != nil {
		return false, fmt.Errorf("marshal bot widget query answer: %w", err)
	}
	pushJSON, err := json.Marshal(store.BotWidgetQueryAnswerPush{QueryID: queryID, BotUserID: botUserID, Answer: answer})
	if err != nil {
		return false, fmt.Errorf("marshal bot widget query answer push: %w", err)
	}
	result, err := resolveBotWidgetQueryScript.Run(ctx, s.c, []string{botWidgetQueryKey(queryID)},
		strconv.FormatInt(botUserID, 10), answerJSON, botWidgetQueryAnswerChannel, pushJSON).Int64()
	if err != nil {
		return false, fmt.Errorf("resolve bot widget query: %w", err)
	}
	return result == 1, nil
}

func (s *WidgetQueryStore) GetBotWidgetQueryAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotWidgetQueryAnswer, bool, error) {
	if s == nil || s.c == nil || botUserID <= 0 || queryID == 0 {
		return domain.BotWidgetQueryAnswer{}, false, nil
	}
	values, err := s.c.HMGet(ctx, botWidgetQueryKey(queryID), "bot_user_id", "answer").Result()
	if err != nil {
		return domain.BotWidgetQueryAnswer{}, false, fmt.Errorf("get bot widget query answer: %w", err)
	}
	if len(values) != 2 || values[0] == nil || values[1] == nil || fmt.Sprint(values[0]) != strconv.FormatInt(botUserID, 10) {
		return domain.BotWidgetQueryAnswer{}, false, nil
	}
	var answer domain.BotWidgetQueryAnswer
	if err := json.Unmarshal([]byte(fmt.Sprint(values[1])), &answer); err != nil {
		return domain.BotWidgetQueryAnswer{}, false, fmt.Errorf("decode bot widget query answer: %w", err)
	}
	return answer, true, nil
}

var deleteBotWidgetQueryScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'bot_user_id') ~= ARGV[1] then
  return 0
end
return redis.call('DEL', KEYS[1])
`)

func (s *WidgetQueryStore) DeleteBotWidgetQueryPending(ctx context.Context, botUserID, queryID int64) error {
	if s == nil || s.c == nil || botUserID <= 0 || queryID == 0 {
		return nil
	}
	if _, err := deleteBotWidgetQueryScript.Run(ctx, s.c, []string{botWidgetQueryKey(queryID)}, strconv.FormatInt(botUserID, 10)).Result(); err != nil && err != redis.Nil {
		return fmt.Errorf("delete bot widget query pending: %w", err)
	}
	return nil
}

func (s *WidgetQueryStore) SubscribeBotWidgetQueryAnswers(ctx context.Context, handle func(context.Context, store.BotWidgetQueryAnswerPush)) error {
	if s == nil || s.c == nil || handle == nil {
		return nil
	}
	pubsub := s.c.Subscribe(ctx, botWidgetQueryAnswerChannel)
	defer pubsub.Close()
	if _, err := pubsub.Receive(ctx); err != nil {
		return fmt.Errorf("subscribe bot widget query answers: %w", err)
	}
	channel := pubsub.Channel(redis.WithChannelSize(256))
	for {
		select {
		case <-ctx.Done():
			return nil
		case message, ok := <-channel:
			if !ok {
				return nil
			}
			var push store.BotWidgetQueryAnswerPush
			if err := json.Unmarshal([]byte(message.Payload), &push); err != nil || push.QueryID == 0 || push.BotUserID <= 0 {
				continue
			}
			handle(ctx, push)
		}
	}
}
