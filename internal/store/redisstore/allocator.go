package redisstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/store"
)

// BoxIDAllocator 用 Redis INCR 分配 owner 视角的 message box id。
type BoxIDAllocator struct {
	counter counterAllocator
}

// ChannelIDAllocator 用 Redis INCR 分配全局 channel/supergroup id。
type ChannelIDAllocator struct {
	counter counterAllocator
}

// ChannelMessageIDAllocator 用 Redis INCR 分配 channel 维度 message id。
type ChannelMessageIDAllocator struct {
	counter counterAllocator
}

// SecretChatIDAllocator 用 Redis INCR 分配全局 secret chat id（int32 量级）。
type SecretChatIDAllocator struct {
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

// NewBoxIDAllocator 创建 Redis-backed message box id allocator。
func NewBoxIDAllocator(c *redis.Client, source store.CounterSource) *BoxIDAllocator {
	return &BoxIDAllocator{counter: counterAllocator{
		c:      c,
		source: source,
		key:    boxIDKey,
		name:   "box_id",
	}}
}

// NewChannelIDAllocator 创建 Redis-backed channel id allocator。
func NewChannelIDAllocator(c *redis.Client, source store.CounterSource) *ChannelIDAllocator {
	return &ChannelIDAllocator{counter: counterAllocator{
		c:      c,
		source: source,
		key:    channelIDKey,
		name:   "channel_id",
	}}
}

// NewChannelMessageIDAllocator 创建 Redis-backed channel message id allocator。
func NewChannelMessageIDAllocator(c *redis.Client, source store.CounterSource) *ChannelMessageIDAllocator {
	return &ChannelMessageIDAllocator{counter: counterAllocator{
		c:      c,
		source: source,
		key:    channelMessageIDKey,
		name:   "channel_msg_id",
	}}
}

// NewSecretChatIDAllocator 创建 Redis-backed secret chat id allocator。
func NewSecretChatIDAllocator(c *redis.Client, source store.CounterSource) *SecretChatIDAllocator {
	return &SecretChatIDAllocator{counter: counterAllocator{
		c:      c,
		source: source,
		key:    secretChatIDKey,
		name:   "secret_chat_id",
	}}
}

func boxIDKey(userID int64) string {
	return fmt.Sprintf("counter:box_id:{%d}", userID)
}

func channelIDKey(_ int64) string {
	return "counter:channel_id"
}

func secretChatIDKey(_ int64) string {
	return "counter:secret_chat_id"
}

func channelMessageIDKey(channelID int64) string {
	return fmt.Sprintf("counter:channel_msg_id:{%d}", channelID)
}

func (a *BoxIDAllocator) NextBoxID(ctx context.Context, userID int64) (int, error) {
	v, err := a.counter.next(ctx, userID)
	return int(v), err
}

func (a *BoxIDAllocator) CurrentBoxID(ctx context.Context, userID int64) (int, error) {
	v, err := a.counter.current(ctx, userID)
	return int(v), err
}

// EnsureBoxIDCounter recovers the Redis counter from durable storage if it is
// missing, without allocating a visible message id.
func (a *BoxIDAllocator) EnsureBoxIDCounter(ctx context.Context, userID int64) error {
	_, err := a.CurrentBoxID(ctx, userID)
	return err
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

// NextChannelIDAtLeast 把计数器至少顶到 floor 后再分配下一个 id。
// 用于撞主键自愈：Redis 快照回退或测试 fallback 分配器绕过 Redis 写库
// 后，计数器可能落后于 channels 表真实最大 id。
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

func (a *SecretChatIDAllocator) NextSecretChatID(ctx context.Context) (int, error) {
	v, err := a.counter.next(ctx, 1)
	return int(v), err
}

// NextSecretChatIDAtLeast 把计数器至少顶到 floor 后再分配下一个 id（撞 chat_id
// 主键自愈：Redis 快照回退或外部写库后计数器落后于 secret_chats 表最大 id）。
func (a *SecretChatIDAllocator) NextSecretChatIDAtLeast(ctx context.Context, floor int) (int, error) {
	if a.counter.c == nil {
		return 0, fmt.Errorf("redis secret_chat_id counter: nil client")
	}
	v, err := counterNextAtLeastScript.Run(ctx, a.counter.c, []string{secretChatIDKey(1)}, floor).Int64()
	if err != nil {
		return 0, fmt.Errorf("redis next-at-least secret_chat_id counter: %w", err)
	}
	return int(v), nil
}

func (a *SecretChatIDAllocator) CurrentSecretChatID(ctx context.Context) (int, error) {
	v, err := a.counter.current(ctx, 1)
	return int(v), err
}

func (a *ChannelMessageIDAllocator) NextChannelMessageID(ctx context.Context, channelID int64) (int, error) {
	v, err := a.counter.next(ctx, channelID)
	return int(v), err
}

func (a *ChannelMessageIDAllocator) CurrentChannelMessageID(ctx context.Context, channelID int64) (int, error) {
	v, err := a.counter.current(ctx, channelID)
	return int(v), err
}

// EnsureChannelMessageIDCounter recovers the Redis counter from durable storage
// if it is missing, without allocating a visible channel message id.
func (a *ChannelMessageIDAllocator) EnsureChannelMessageIDCounter(ctx context.Context, channelID int64) error {
	_, err := a.CurrentChannelMessageID(ctx, channelID)
	return err
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
