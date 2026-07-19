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

const botCallbackAnswerChannel = "telesrv:bot_callback:answers"

type BotCallbackRegistryStore struct {
	c redis.UniversalClient
}

func NewBotCallbackRegistryStore(c redis.UniversalClient) *BotCallbackRegistryStore {
	return &BotCallbackRegistryStore{c: c}
}

func botCallbackKey(queryID int64) string {
	return fmt.Sprintf("telesrv:bot_callback:%d", queryID)
}

var putBotCallbackScript = redis.NewScript(`
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

func (s *BotCallbackRegistryStore) PutBotCallbackPending(ctx context.Context, pending store.BotCallbackPending, ttl time.Duration) (bool, error) {
	if s == nil || s.c == nil || pending.QueryID == 0 || pending.BotUserID <= 0 || pending.UserID <= 0 || ttl <= 0 {
		return false, fmt.Errorf("invalid bot callback pending")
	}
	createdAt := pending.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	result, err := putBotCallbackScript.Run(ctx, s.c, []string{botCallbackKey(pending.QueryID)},
		pending.BotUserID, pending.UserID, createdAt.UnixNano(), ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("put bot callback pending: %w", err)
	}
	return result == 1, nil
}

var resolveBotCallbackScript = redis.NewScript(`
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

func (s *BotCallbackRegistryStore) ResolveBotCallback(ctx context.Context, botUserID, queryID int64, answer domain.BotCallbackAnswer) (bool, error) {
	if s == nil || s.c == nil || botUserID <= 0 || queryID == 0 {
		return false, nil
	}
	answerJSON, err := json.Marshal(answer)
	if err != nil {
		return false, fmt.Errorf("marshal bot callback answer: %w", err)
	}
	pushJSON, err := json.Marshal(store.BotCallbackAnswerPush{QueryID: queryID, BotUserID: botUserID, Answer: answer})
	if err != nil {
		return false, fmt.Errorf("marshal bot callback answer push: %w", err)
	}
	result, err := resolveBotCallbackScript.Run(ctx, s.c, []string{botCallbackKey(queryID)},
		strconv.FormatInt(botUserID, 10), answerJSON, botCallbackAnswerChannel, pushJSON).Int64()
	if err != nil {
		return false, fmt.Errorf("resolve bot callback: %w", err)
	}
	return result == 1, nil
}

func (s *BotCallbackRegistryStore) GetBotCallbackAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotCallbackAnswer, bool, error) {
	if s == nil || s.c == nil || botUserID <= 0 || queryID == 0 {
		return domain.BotCallbackAnswer{}, false, nil
	}
	values, err := s.c.HMGet(ctx, botCallbackKey(queryID), "bot_user_id", "answer").Result()
	if err != nil {
		return domain.BotCallbackAnswer{}, false, fmt.Errorf("get bot callback answer: %w", err)
	}
	if len(values) != 2 || values[0] == nil || values[1] == nil || fmt.Sprint(values[0]) != strconv.FormatInt(botUserID, 10) {
		return domain.BotCallbackAnswer{}, false, nil
	}
	var answer domain.BotCallbackAnswer
	if err := json.Unmarshal([]byte(fmt.Sprint(values[1])), &answer); err != nil {
		return domain.BotCallbackAnswer{}, false, fmt.Errorf("decode bot callback answer: %w", err)
	}
	return answer, true, nil
}

var deleteBotCallbackScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'bot_user_id') ~= ARGV[1] then
  return 0
end
return redis.call('DEL', KEYS[1])
`)

func (s *BotCallbackRegistryStore) DeleteBotCallbackPending(ctx context.Context, botUserID, queryID int64) error {
	if s == nil || s.c == nil || botUserID <= 0 || queryID == 0 {
		return nil
	}
	if _, err := deleteBotCallbackScript.Run(ctx, s.c, []string{botCallbackKey(queryID)}, strconv.FormatInt(botUserID, 10)).Result(); err != nil && err != redis.Nil {
		return fmt.Errorf("delete bot callback pending: %w", err)
	}
	return nil
}

func (s *BotCallbackRegistryStore) SubscribeBotCallbackAnswers(ctx context.Context, handle func(context.Context, store.BotCallbackAnswerPush)) error {
	if s == nil || s.c == nil || handle == nil {
		return nil
	}
	pubsub := s.c.Subscribe(ctx, botCallbackAnswerChannel)
	defer pubsub.Close()
	if _, err := pubsub.Receive(ctx); err != nil {
		return fmt.Errorf("subscribe bot callback answers: %w", err)
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
			var push store.BotCallbackAnswerPush
			if err := json.Unmarshal([]byte(message.Payload), &push); err != nil || push.QueryID == 0 || push.BotUserID <= 0 {
				continue
			}
			handle(ctx, push)
		}
	}
}
