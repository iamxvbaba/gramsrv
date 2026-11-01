package store

import (
	"context"
	"time"
)

// RateLimiter provides lightweight windowed rate limiting. It returns retryAfterSeconds for RPC
// to map to FLOOD_WAIT_X.
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfterSeconds int, err error)
	AllowN(ctx context.Context, key string, cost, limit int, window time.Duration) (allowed bool, retryAfterSeconds int, err error)
}
