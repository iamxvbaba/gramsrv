package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/store"
)

// CodeStore 用 Redis 实现 store.CodeStore（验证码带 TTL 自动过期）。
type CodeStore struct {
	c *redis.Client
}

// NewCodeStore 创建 Redis CodeStore。
func NewCodeStore(c *redis.Client) *CodeStore {
	return &CodeStore{c: c}
}

func codeKey(hash string) string { return "phonecode:" + hash }

func (s *CodeStore) Set(ctx context.Context, hash string, code store.PhoneCode, ttl time.Duration) error {
	v, err := json.Marshal(code)
	if err != nil {
		return fmt.Errorf("marshal phone code: %w", err)
	}
	if err := s.c.Set(ctx, codeKey(hash), v, ttl).Err(); err != nil {
		return fmt.Errorf("redis set phone code: %w", err)
	}
	return nil
}

func (s *CodeStore) Get(ctx context.Context, hash string) (store.PhoneCode, bool, error) {
	raw, err := s.c.Get(ctx, codeKey(hash)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return store.PhoneCode{}, false, nil
		}
		return store.PhoneCode{}, false, fmt.Errorf("redis get phone code: %w", err)
	}
	var code store.PhoneCode
	if err := json.Unmarshal(raw, &code); err != nil {
		return store.PhoneCode{}, false, fmt.Errorf("unmarshal phone code: %w", err)
	}
	return code, true, nil
}

func (s *CodeStore) Update(ctx context.Context, hash string, code store.PhoneCode) error {
	key := codeKey(hash)
	ttl, err := s.c.TTL(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("redis ttl phone code: %w", err)
	}
	if ttl <= 0 {
		return nil
	}
	v, err := json.Marshal(code)
	if err != nil {
		return fmt.Errorf("marshal phone code: %w", err)
	}
	if err := s.c.Set(ctx, key, v, ttl).Err(); err != nil {
		return fmt.Errorf("redis update phone code: %w", err)
	}
	return nil
}

func (s *CodeStore) Del(ctx context.Context, hash string) error {
	return s.c.Del(ctx, codeKey(hash)).Err()
}
