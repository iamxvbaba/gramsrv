// Package redisstore implements high-frequency, ephemeral, rebuildable short-lived state,
// caches, counters and rate limits with Redis.
//
// See docs/persistence-layer.md §1 for the boundary of responsibility: Redis stores "state and
// counts" whose loss can be recovered from PG/the protocol.
package redisstore

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultOpenPingTimeout = 5 * time.Second

// Open establishes a Redis connection at addr and verifies it with a bounded PING; the returned client keeps go-redis's default retry policy.
func Open(ctx context.Context, addr, password string, db int) (*redis.Client, error) {
	return open(ctx, addr, password, db, defaultOpenPingTimeout)
}

func open(ctx context.Context, addr, password string, db int, pingTimeout time.Duration) (*redis.Client, error) {
	c := redis.NewClient(&redis.Options{
		Addr:                  addr,
		Password:              password,
		DB:                    db,
		ContextTimeoutEnabled: true,
	})
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := c.Ping(pingCtx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("redis ping %s: %w", addr, err)
	}
	return c, nil
}
