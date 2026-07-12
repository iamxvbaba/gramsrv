// Package redisstore 用 Redis 实现高频、易失且可重建的短状态、缓存、计数器与限流。
//
// 职责边界见 docs/persistence-layer.md §1：Redis 存「态与计数」，丢失可由 PG/协议恢复。
package redisstore

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Open 按地址建立 Redis 连接并 ping 验证。
func Open(ctx context.Context, addr, password string, db int) (*redis.Client, error) {
	c := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("redis ping %s: %w", addr, err)
	}
	return c, nil
}
