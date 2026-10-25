package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/domain"
)

const botPreCheckoutWakeTTL = 30 * time.Second

// BotPreCheckoutRegistryStore is the Redis implementation of
// store.BotPreCheckoutRegistryStore, shaped like BotCallbackRegistryStore.
type BotPreCheckoutRegistryStore struct {
	c redis.UniversalClient
}

func NewBotPreCheckoutRegistryStore(c redis.UniversalClient) *BotPreCheckoutRegistryStore {
	return &BotPreCheckoutRegistryStore{c: c}
}

func botPreCheckoutKey(queryID int64) string {
	return fmt.Sprintf("telesrv:bot_precheckout:%d", queryID)
}

func botPreCheckoutWakeKey(queryID int64) string {
	return fmt.Sprintf("telesrv:bot_precheckout:%d:wake", queryID)
}

var putBotPreCheckoutScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) ~= 0 then
  return 0
end
redis.call('HSET', KEYS[1], 'bot_user_id', ARGV[1], 'user_id', ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1
`)

func (s *BotPreCheckoutRegistryStore) PutBotPreCheckoutPending(ctx context.Context, queryID, botUserID, userID int64, ttl time.Duration) (bool, error) {
	if s == nil || s.c == nil || queryID == 0 || botUserID <= 0 || userID <= 0 || ttl <= 0 {
		return false, fmt.Errorf("invalid bot pre-checkout pending")
	}
	result, err := putBotPreCheckoutScript.Run(ctx, s.c, []string{botPreCheckoutKey(queryID)},
		botUserID, userID, ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("put bot pre-checkout pending: %w", err)
	}
	return result == 1, nil
}

var resolveBotPreCheckoutScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'bot_user_id') ~= ARGV[1] then
  return 0
end
if redis.call('HEXISTS', KEYS[1], 'answer') ~= 0 then
  return 0
end
redis.call('HSET', KEYS[1], 'answer', ARGV[2])
redis.call('LPUSH', KEYS[2], '1')
redis.call('PEXPIRE', KEYS[2], ARGV[3])
return 1
`)

func (s *BotPreCheckoutRegistryStore) ResolveBotPreCheckout(ctx context.Context, botUserID, queryID int64, answer domain.BotPreCheckoutAnswer) (bool, error) {
	if s == nil || s.c == nil || botUserID <= 0 || queryID == 0 {
		return false, nil
	}
	answerJSON, err := json.Marshal(answer)
	if err != nil {
		return false, fmt.Errorf("marshal bot pre-checkout answer: %w", err)
	}
	result, err := resolveBotPreCheckoutScript.Run(ctx, s.c, []string{botPreCheckoutKey(queryID), botPreCheckoutWakeKey(queryID)},
		strconv.FormatInt(botUserID, 10), answerJSON, botPreCheckoutWakeTTL.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("resolve bot pre-checkout: %w", err)
	}
	return result == 1, nil
}

func (s *BotPreCheckoutRegistryStore) getAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotPreCheckoutAnswer, bool, error) {
	values, err := s.c.HMGet(ctx, botPreCheckoutKey(queryID), "bot_user_id", "answer").Result()
	if err != nil {
		return domain.BotPreCheckoutAnswer{}, false, fmt.Errorf("get bot pre-checkout answer: %w", err)
	}
	if len(values) != 2 || values[0] == nil || values[1] == nil || fmt.Sprint(values[0]) != strconv.FormatInt(botUserID, 10) {
		return domain.BotPreCheckoutAnswer{}, false, nil
	}
	return decodeBotPreCheckoutAnswer(fmt.Sprint(values[1]))
}

func decodeBotPreCheckoutAnswer(raw string) (domain.BotPreCheckoutAnswer, bool, error) {
	var answer domain.BotPreCheckoutAnswer
	if err := json.Unmarshal([]byte(raw), &answer); err != nil {
		return domain.BotPreCheckoutAnswer{}, false, fmt.Errorf("decode bot pre-checkout answer: %w", err)
	}
	return answer, true, nil
}

// WaitBotPreCheckoutAnswer blocks until the owning bot answers or ctx ends.
// The caller bounds ctx with the pre-checkout window.
func (s *BotPreCheckoutRegistryStore) WaitBotPreCheckoutAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotPreCheckoutAnswer, bool, error) {
	if s == nil || s.c == nil || botUserID <= 0 || queryID == 0 {
		return domain.BotPreCheckoutAnswer{}, false, nil
	}
	for {
		answer, found, err := s.getAnswer(ctx, botUserID, queryID)
		if err != nil || found {
			return answer, found, err
		}
		_, err = s.c.BLPop(ctx, 0, botPreCheckoutWakeKey(queryID)).Result()
		if err == nil {
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return domain.BotPreCheckoutAnswer{}, false, nil
		}
		if err == redis.Nil {
			continue
		}
		return domain.BotPreCheckoutAnswer{}, false, fmt.Errorf("wait bot pre-checkout answer: %w", err)
	}
}

var takeBotPreCheckoutScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'bot_user_id') ~= ARGV[1] then
  return false
end
local answer = redis.call('HGET', KEYS[1], 'answer')
redis.call('DEL', KEYS[1], KEYS[2])
return answer
`)

// TakeBotPreCheckout deletes the record and returns the answer that already won,
// if any, in one atomic step.
func (s *BotPreCheckoutRegistryStore) TakeBotPreCheckout(ctx context.Context, botUserID, queryID int64) (domain.BotPreCheckoutAnswer, bool, error) {
	if s == nil || s.c == nil || botUserID <= 0 || queryID == 0 {
		return domain.BotPreCheckoutAnswer{}, false, nil
	}
	raw, err := takeBotPreCheckoutScript.Run(ctx, s.c, []string{botPreCheckoutKey(queryID), botPreCheckoutWakeKey(queryID)},
		strconv.FormatInt(botUserID, 10)).Text()
	if errors.Is(err, redis.Nil) {
		return domain.BotPreCheckoutAnswer{}, false, nil
	}
	if err != nil {
		return domain.BotPreCheckoutAnswer{}, false, fmt.Errorf("take bot pre-checkout: %w", err)
	}
	return decodeBotPreCheckoutAnswer(raw)
}
