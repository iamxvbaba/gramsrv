package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// answerPush is the shared cross-node pub/sub envelope for bot callbacks and bot
// widget queries. Field names match the per-feature push structs, so the wire
// format is unchanged.
type answerPush[A any] struct {
	QueryID   int64
	BotUserID int64
	Answer    A
}

// answerQueryStore is the shared Redis CAS used by BotCallbackRegistryStore and
// WidgetQueryStore: a per-query hash holds the owning bot and, once resolved,
// the answer, which is also broadcast over a pub/sub channel. The feature label
// only affects error text.
type answerQueryStore[A any] struct {
	c       redis.UniversalClient
	prefix  string
	channel string
	label   string
}

func (s *answerQueryStore[A]) key(queryID int64) string {
	return fmt.Sprintf("%s:%d", s.prefix, queryID)
}

var putAnswerPendingScript = redis.NewScript(`
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

var resolveAnswerScript = redis.NewScript(`
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

var deleteAnswerPendingScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'bot_user_id') ~= ARGV[1] then
  return 0
end
return redis.call('DEL', KEYS[1])
`)

func (s *answerQueryStore[A]) putPending(ctx context.Context, queryID, botUserID, userID int64, createdAt time.Time, ttl time.Duration) (bool, error) {
	if s.c == nil || queryID == 0 || botUserID <= 0 || userID <= 0 || ttl <= 0 {
		return false, fmt.Errorf("invalid %s pending", s.label)
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	result, err := putAnswerPendingScript.Run(ctx, s.c, []string{s.key(queryID)},
		botUserID, userID, createdAt.UnixNano(), ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("put %s pending: %w", s.label, err)
	}
	return result == 1, nil
}

func (s *answerQueryStore[A]) resolve(ctx context.Context, botUserID, queryID int64, answer A) (bool, error) {
	if s.c == nil || botUserID <= 0 || queryID == 0 {
		return false, nil
	}
	answerJSON, err := json.Marshal(answer)
	if err != nil {
		return false, fmt.Errorf("marshal %s answer: %w", s.label, err)
	}
	pushJSON, err := json.Marshal(answerPush[A]{QueryID: queryID, BotUserID: botUserID, Answer: answer})
	if err != nil {
		return false, fmt.Errorf("marshal %s answer push: %w", s.label, err)
	}
	result, err := resolveAnswerScript.Run(ctx, s.c, []string{s.key(queryID)},
		strconv.FormatInt(botUserID, 10), answerJSON, s.channel, pushJSON).Int64()
	if err != nil {
		return false, fmt.Errorf("resolve %s: %w", s.label, err)
	}
	return result == 1, nil
}

func (s *answerQueryStore[A]) answer(ctx context.Context, botUserID, queryID int64) (A, bool, error) {
	var zero A
	if s.c == nil || botUserID <= 0 || queryID == 0 {
		return zero, false, nil
	}
	values, err := s.c.HMGet(ctx, s.key(queryID), "bot_user_id", "answer").Result()
	if err != nil {
		return zero, false, fmt.Errorf("get %s answer: %w", s.label, err)
	}
	if len(values) != 2 || values[0] == nil || values[1] == nil || fmt.Sprint(values[0]) != strconv.FormatInt(botUserID, 10) {
		return zero, false, nil
	}
	var answer A
	if err := json.Unmarshal([]byte(fmt.Sprint(values[1])), &answer); err != nil {
		return zero, false, fmt.Errorf("decode %s answer: %w", s.label, err)
	}
	return answer, true, nil
}

func (s *answerQueryStore[A]) deletePending(ctx context.Context, botUserID, queryID int64) error {
	if s.c == nil || botUserID <= 0 || queryID == 0 {
		return nil
	}
	if _, err := deleteAnswerPendingScript.Run(ctx, s.c, []string{s.key(queryID)}, strconv.FormatInt(botUserID, 10)).Result(); err != nil && err != redis.Nil {
		return fmt.Errorf("delete %s pending: %w", s.label, err)
	}
	return nil
}

func (s *answerQueryStore[A]) subscribe(ctx context.Context, handle func(context.Context, answerPush[A])) error {
	if s.c == nil || handle == nil {
		return nil
	}
	pubsub := s.c.Subscribe(ctx, s.channel)
	defer pubsub.Close()
	if _, err := pubsub.Receive(ctx); err != nil {
		return fmt.Errorf("subscribe %s answers: %w", s.label, err)
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
			var push answerPush[A]
			if err := json.Unmarshal([]byte(message.Payload), &push); err != nil || push.QueryID == 0 || push.BotUserID <= 0 {
				continue
			}
			handle(ctx, push)
		}
	}
}
