package redisstore

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter implements fixed-window rate limiting with Redis INCR + TTL.
type RateLimiter struct {
	c *redis.Client
}

// NewRateLimiter creates a Redis-backed RateLimiter.
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
	count, ttlMillis, err := decodeRateLimitIncrementResult(value)
	if err != nil {
		return false, 0, err
	}
	if count <= int64(limit) {
		return true, 0, nil
	}
	// Redis PTTL returns 0 when less than one millisecond remains. That is a
	// valid fixed-window boundary, not a corrupt result. Round it up to the
	// smallest protocol-safe FLOOD_WAIT instead of leaking a transient 500.
	retry := (ttlMillis + 999) / 1000
	if retry <= 0 {
		retry = 1
	}
	return false, int(retry), nil
}

func decodeRateLimitIncrementResult(value any) (count int64, ttlMillis int64, err error) {
	items, ok := value.([]interface{})
	if !ok || len(items) != 2 {
		return 0, 0, fmt.Errorf("redis increment rate limit: unexpected result %T", value)
	}
	count, countOK := items[0].(int64)
	ttlMillis, ttlOK := items[1].(int64)
	if !countOK || !ttlOK || count <= 0 || ttlMillis < 0 {
		return 0, 0, fmt.Errorf("redis increment rate limit: invalid result %#v", items)
	}
	return count, ttlMillis, nil
}
