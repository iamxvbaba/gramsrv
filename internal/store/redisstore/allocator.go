package redisstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/store"
)

// BoxIDAllocator uses Redis INCR to allocate owner-visible message box ids.
type BoxIDAllocator struct {
	counter counterAllocator
}

var _ store.DistributedBoxIDAllocator = (*BoxIDAllocator)(nil)

// DistributedBoxIDAllocation marks Redis INCR reservations as safe for the
// cross-process private-send microbatch path.
func (*BoxIDAllocator) DistributedBoxIDAllocation() {}

// ChannelIDAllocator uses Redis INCR to allocate global channel/supergroup ids.
type ChannelIDAllocator struct {
	counter counterAllocator
}

// ChannelMessageIDAllocator uses Redis INCR to allocate channel-scoped message ids.
type ChannelMessageIDAllocator struct {
	counter counterAllocator
}

type counterAllocator struct {
	c      *redis.Client
	source store.CounterSource
	key    func(int64) string
	name   string
}

const missingCounterSentinel int64 = -1

var (
	counterNextScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if current then
  return redis.call("INCR", KEYS[1])
end
return -1
`)

	counterRecoverCurrentScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if current then
  return tonumber(current)
end
redis.call("SET", KEYS[1], ARGV[1])
return tonumber(ARGV[1])
`)

	counterRecoverNextScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then
  redis.call("SET", KEYS[1], ARGV[1])
end
return redis.call("INCR", KEYS[1])
`)

	counterNextAtLeastScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if (not current) or tonumber(current) < tonumber(ARGV[1]) then
  redis.call("SET", KEYS[1], ARGV[1])
end
return redis.call("INCR", KEYS[1])
`)

	counterSetAtLeastScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if (not current) or tonumber(current) < tonumber(ARGV[1]) then
  redis.call("SET", KEYS[1], ARGV[1])
  return tonumber(ARGV[1])
end
return tonumber(current)
`)
)

// NewBoxIDAllocator creates a Redis-backed message box id allocator.
func NewBoxIDAllocator(c *redis.Client, source store.CounterSource) *BoxIDAllocator {
	return &BoxIDAllocator{counter: counterAllocator{
		c:      c,
		source: source,
		key:    boxIDKey,
		name:   "box_id",
	}}
}

// NewChannelIDAllocator creates a Redis-backed channel id allocator.
func NewChannelIDAllocator(c *redis.Client, source store.CounterSource) *ChannelIDAllocator {
	return &ChannelIDAllocator{counter: counterAllocator{
		c:      c,
		source: source,
		key:    channelIDKey,
		name:   "channel_id",
	}}
}

// NewChannelMessageIDAllocator creates a Redis-backed channel message id allocator.
func NewChannelMessageIDAllocator(c *redis.Client, source store.CounterSource) *ChannelMessageIDAllocator {
	return &ChannelMessageIDAllocator{counter: counterAllocator{
		c:      c,
		source: source,
		key:    channelMessageIDKey,
		name:   "channel_msg_id",
	}}
}

func boxIDKey(userID int64) string {
	return fmt.Sprintf("counter:box_id:{%d}", userID)
}

func channelIDKey(_ int64) string {
	return "counter:channel_id"
}

func channelMessageIDKey(channelID int64) string {
	return fmt.Sprintf("counter:channel_msg_id:{%d}", channelID)
}

func (a *BoxIDAllocator) NextBoxID(ctx context.Context, userID int64) (int, error) {
	v, err := a.counter.next(ctx, userID)
	return int(v), err
}

// NextBoxIDs allocates every distinct owner in one Redis pipeline. Cold
// counters use one durable batch read and one recovery pipeline; the batch API
// never degrades into per-user network calls.
func (a *BoxIDAllocator) NextBoxIDs(ctx context.Context, userIDs []int64) (map[int64]int, error) {
	if a == nil || a.counter.c == nil {
		return nil, fmt.Errorf("redis box_id counter: nil client")
	}
	unique := make([]int64, 0, len(userIDs))
	keys := make([]string, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 {
			return nil, fmt.Errorf("redis box_id counter: invalid user id %d", userID)
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		key, err := a.counter.validatedKey(userID)
		if err != nil {
			return nil, err
		}
		unique = append(unique, userID)
		keys = append(keys, key)
	}
	if len(unique) == 0 {
		return map[int64]int{}, nil
	}

	commands := make([]*redis.Cmd, len(unique))
	if _, err := a.counter.c.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for i, key := range keys {
			commands[i] = counterNextScript.Eval(ctx, pipe, []string{key})
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("redis batch next box_id counters: %w", err)
	}

	out := make(map[int64]int, len(unique))
	missingUsers := make([]int64, 0, len(unique))
	missingKeys := make([]string, 0, len(unique))
	for i, command := range commands {
		value, err := command.Int64()
		if err != nil {
			return nil, fmt.Errorf("redis batch next box_id counter for %d: %w", unique[i], err)
		}
		if value == missingCounterSentinel {
			missingUsers = append(missingUsers, unique[i])
			missingKeys = append(missingKeys, keys[i])
			continue
		}
		out[unique[i]] = int(value)
	}
	if len(missingUsers) == 0 {
		return out, nil
	}

	recovered, err := a.counter.recoveredBatch(ctx, missingUsers)
	if err != nil {
		return nil, err
	}
	recoveryCommands := make([]*redis.Cmd, len(missingUsers))
	if _, err := a.counter.c.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for i, key := range missingKeys {
			floor, ok := recovered[missingUsers[i]]
			if !ok {
				return fmt.Errorf("durable source omitted user %d", missingUsers[i])
			}
			recoveryCommands[i] = counterRecoverNextScript.Eval(ctx, pipe, []string{key}, floor)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("redis batch recover-next box_id counters: %w", err)
	}
	for i, command := range recoveryCommands {
		value, err := command.Int64()
		if err != nil {
			return nil, fmt.Errorf("redis batch recover-next box_id counter for %d: %w", missingUsers[i], err)
		}
		out[missingUsers[i]] = int(value)
	}
	return out, nil
}

func (a *BoxIDAllocator) CurrentBoxID(ctx context.Context, userID int64) (int, error) {
	v, err := a.counter.current(ctx, userID)
	return int(v), err
}

// BumpBoxIDAtLeast advances the Redis box id counter to at least floor without
// allocating a visible id. It is a cold-path self-heal for Redis counters that
// lag behind message_boxes after external/dev writes.
func (a *BoxIDAllocator) BumpBoxIDAtLeast(ctx context.Context, userID int64, floor int) error {
	if a.counter.c == nil {
		return fmt.Errorf("redis box_id counter: nil client")
	}
	if _, err := counterSetAtLeastScript.Run(ctx, a.counter.c, []string{boxIDKey(userID)}, floor).Int64(); err != nil {
		return fmt.Errorf("redis set-at-least box_id counter: %w", err)
	}
	return nil
}

func (a *ChannelIDAllocator) NextChannelID(ctx context.Context) (int64, error) {
	return a.counter.next(ctx, 1)
}

// NextChannelIDAtLeast bumps the counter to at least floor before allocating the next id.
// Used for primary-key collision self-healing: after a Redis snapshot rollback or when a test
// fallback allocator bypasses Redis and writes to the DB, the counter may lag behind the real
// max id in the channels table.
func (a *ChannelIDAllocator) NextChannelIDAtLeast(ctx context.Context, floor int64) (int64, error) {
	if a.counter.c == nil {
		return 0, fmt.Errorf("redis channel_id counter: nil client")
	}
	v, err := counterNextAtLeastScript.Run(ctx, a.counter.c, []string{channelIDKey(1)}, floor).Int64()
	if err != nil {
		return 0, fmt.Errorf("redis next-at-least channel_id counter: %w", err)
	}
	return v, nil
}

func (a *ChannelIDAllocator) CurrentChannelID(ctx context.Context) (int64, error) {
	return a.counter.current(ctx, 1)
}

func (a *ChannelMessageIDAllocator) NextChannelMessageID(ctx context.Context, channelID int64) (int, error) {
	v, err := a.counter.next(ctx, channelID)
	return int(v), err
}

func (a *ChannelMessageIDAllocator) CurrentChannelMessageID(ctx context.Context, channelID int64) (int, error) {
	v, err := a.counter.current(ctx, channelID)
	return int(v), err
}

func (a counterAllocator) next(ctx context.Context, userID int64) (int64, error) {
	key, err := a.validatedKey(userID)
	if err != nil {
		return 0, err
	}
	v, err := counterNextScript.Run(ctx, a.c, []string{key}).Int64()
	if err != nil {
		return 0, fmt.Errorf("redis next %s counter: %w", a.name, err)
	}
	if v != missingCounterSentinel {
		return v, nil
	}
	recovered, err := a.recovered(ctx, userID)
	if err != nil {
		return 0, err
	}
	v, err = counterRecoverNextScript.Run(ctx, a.c, []string{key}, recovered).Int64()
	if err != nil {
		return 0, fmt.Errorf("redis recover-next %s counter: %w", a.name, err)
	}
	return v, nil
}

func (a counterAllocator) current(ctx context.Context, userID int64) (int64, error) {
	key, err := a.validatedKey(userID)
	if err != nil {
		return 0, err
	}
	v, err := a.c.Get(ctx, key).Int64()
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, redis.Nil) {
		return 0, fmt.Errorf("redis get %s counter: %w", a.name, err)
	}
	recovered, err := a.recovered(ctx, userID)
	if err != nil {
		return 0, err
	}
	v, err = counterRecoverCurrentScript.Run(ctx, a.c, []string{key}, recovered).Int64()
	if err != nil {
		return 0, fmt.Errorf("redis recover-current %s counter: %w", a.name, err)
	}
	return v, nil
}

func (a counterAllocator) validatedKey(userID int64) (string, error) {
	if userID == 0 {
		return "", fmt.Errorf("redis %s counter: missing user id", a.name)
	}
	if a.c == nil {
		return "", fmt.Errorf("redis %s counter: nil client", a.name)
	}
	return a.key(userID), nil
}

func (a counterAllocator) recovered(ctx context.Context, userID int64) (int, error) {
	recovered := 0
	var err error
	if a.source != nil {
		recovered, err = a.source.Current(ctx, userID)
		if err != nil {
			return 0, fmt.Errorf("recover %s counter: %w", a.name, err)
		}
	}
	return recovered, nil
}

func (a counterAllocator) recoveredBatch(ctx context.Context, userIDs []int64) (map[int64]int, error) {
	if a.source == nil {
		return nil, fmt.Errorf("recover %s counters: missing durable source", a.name)
	}
	recovered, err := a.source.CurrentBatch(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("recover %s counters: %w", a.name, err)
	}
	return recovered, nil
}
