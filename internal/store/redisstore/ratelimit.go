package redisstore

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter 用 Redis INCR + TTL 实现固定窗口限流。
type RateLimiter struct {
	c *redis.Client
}

// NewRateLimiter 创建 Redis-backed RateLimiter。
func NewRateLimiter(c *redis.Client) *RateLimiter {
	return &RateLimiter{c: c}
}

func rateLimitKey(key string) string {
	return "ratelimit:" + key
}

const rateLimitIncrementScript = `
local count = redis.call('INCRBY', KEYS[1], ARGV[1])
local ttl_ms = redis.call('PTTL', KEYS[1])
if ttl_ms < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  ttl_ms = tonumber(ARGV[2])
end
return {count, ttl_ms}
`

func (l *RateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error) {
	return l.AllowN(ctx, key, 1, limit, window)
}

func (l *RateLimiter) AllowN(ctx context.Context, key string, cost, limit int, window time.Duration) (bool, int, error) {
	if cost <= 0 {
		return true, 0, nil
	}
	if limit <= 0 {
		return true, 0, nil
	}
	if window <= 0 {
		window = time.Second
	}
	if l == nil || l.c == nil {
		return false, 0, fmt.Errorf("redis rate limiter: nil client")
	}
	redisKey := rateLimitKey(key)
	windowMillis := window.Milliseconds()
	if windowMillis <= 0 {
		windowMillis = 1
	}
	value, err := l.c.Eval(ctx, rateLimitIncrementScript, []string{redisKey}, cost, windowMillis).Result()
	if err != nil {
		return false, 0, fmt.Errorf("redis increment rate limit: %w", err)
	}
	items, ok := value.([]interface{})
	if !ok || len(items) != 2 {
		return false, 0, fmt.Errorf("redis increment rate limit: unexpected result %T", value)
	}
	count, countOK := items[0].(int64)
	ttlMillis, ttlOK := items[1].(int64)
	if !countOK || !ttlOK || ttlMillis <= 0 {
		return false, 0, fmt.Errorf("redis increment rate limit: invalid result %#v", items)
	}
	if count <= int64(limit) {
		return true, 0, nil
	}
	retry := (ttlMillis + 999) / 1000
	if retry <= 0 {
		retry = 1
	}
	return false, int(retry), nil
}
