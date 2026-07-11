package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const codeKeyPrefix = "phonecode:"

func codeKey(hash string) string { return codeKeyPrefix + hash }

func codeScopeKey(scope store.PhoneCodeScope) string {
	// 不把手机号/auth key 明文放进 Redis key；JSON 仅作为稳定的长度分隔编码输入。
	raw, _ := json.Marshal(scope)
	digest := sha256.Sum256(raw)
	return "phonecodescope:" + hex.EncodeToString(digest[:])
}

const rotateScopedCodeScript = `
local old_hash = redis.call('GET', KEYS[2])
if old_hash and old_hash ~= ARGV[1] then
  redis.call('DEL', ARGV[4] .. old_hash)
end
local ttl_ms = tonumber(ARGV[3])
if ttl_ms and ttl_ms > 0 then
  redis.call('PSETEX', KEYS[1], ttl_ms, ARGV[2])
  redis.call('PSETEX', KEYS[2], ttl_ms, ARGV[1])
else
  redis.call('SET', KEYS[1], ARGV[2])
  redis.call('SET', KEYS[2], ARGV[1])
end
return 1
`

const deleteScopedCodeScript = `
redis.call('DEL', KEYS[1])
if redis.call('GET', KEYS[2]) == ARGV[1] then
  redis.call('DEL', KEYS[2])
end
return 1
`

const consumeScopedCodeScript = `
if redis.call('GET', KEYS[2]) ~= ARGV[1] then
  return false
end
local raw = redis.call('GET', KEYS[1])
if not raw then
  redis.call('DEL', KEYS[2])
  return false
end
local decoded, record = pcall(cjson.decode, raw)
if not decoded or type(record) ~= 'table'
    or tonumber(record.Version or 0) ~= tonumber(ARGV[2]) then
  redis.call('DEL', KEYS[1])
  redis.call('DEL', KEYS[2])
  return false
end
redis.call('DEL', KEYS[1])
redis.call('DEL', KEYS[2])
return raw
`

const updatePhoneCodeScript = `
if redis.call('EXISTS', KEYS[1]) == 0 then
  return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'KEEPTTL')
return 1
`

const deleteUndecodablePhoneCodeScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

func (s *CodeStore) Set(ctx context.Context, hash string, code store.PhoneCode, ttl time.Duration) error {
	revision, err := store.NewPhoneCodeRevisionToken()
	if err != nil {
		return err
	}
	code.Revision = revision
	v, err := json.Marshal(code)
	if err != nil {
		return fmt.Errorf("marshal phone code: %w", err)
	}
	scope := code.Scope()
	if !scope.Valid() {
		if err := s.c.Set(ctx, codeKey(hash), v, ttl).Err(); err != nil {
			return fmt.Errorf("redis set phone code: %w", err)
		}
		return nil
	}
	ttlMillis := ttl.Milliseconds()
	if ttl > 0 && ttlMillis == 0 {
		ttlMillis = 1
	}
	if err := s.c.Eval(
		ctx,
		rotateScopedCodeScript,
		[]string{codeKey(hash), codeScopeKey(scope)},
		hash,
		string(v),
		ttlMillis,
		codeKeyPrefix,
	).Err(); err != nil {
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
		// Version-zero records used JSON numbers for int64 fields. Once those
		// fields became quoted strings, such a record is intentionally unusable;
		// compare-and-delete it so a concurrent Set successor cannot be removed.
		// Its scope cannot be decoded here; a stale scope index is harmless and is
		// removed by the next scoped Set, ConsumeScoped, or VerifyScoped.
		if deleteErr := s.c.Eval(ctx, deleteUndecodablePhoneCodeScript, []string{codeKey(hash)}, string(raw)).Err(); deleteErr != nil {
			return store.PhoneCode{}, false, fmt.Errorf("delete undecodable phone code after %v: %w", err, deleteErr)
		}
		return store.PhoneCode{}, false, nil
	}
	return code, true, nil
}

func (s *CodeStore) Update(ctx context.Context, hash string, code store.PhoneCode) error {
	revision, err := store.NewPhoneCodeRevisionToken()
	if err != nil {
		return err
	}
	code.Revision = revision
	v, err := json.Marshal(code)
	if err != nil {
		return fmt.Errorf("marshal phone code: %w", err)
	}
	if err := s.c.Eval(ctx, updatePhoneCodeScript, []string{codeKey(hash)}, string(v)).Err(); err != nil {
		return fmt.Errorf("redis update phone code: %w", err)
	}
	return nil
}

func (s *CodeStore) Del(ctx context.Context, hash string) error {
	key := codeKey(hash)
	raw, err := s.c.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return s.c.Del(ctx, key).Err()
		}
		return fmt.Errorf("redis get phone code for delete: %w", err)
	}
	var code store.PhoneCode
	if err := json.Unmarshal(raw, &code); err != nil {
		return fmt.Errorf("unmarshal phone code for delete: %w", err)
	}
	scope := code.Scope()
	if !scope.Valid() {
		return s.c.Del(ctx, key).Err()
	}
	if err := s.c.Eval(ctx, deleteScopedCodeScript, []string{key, codeScopeKey(scope)}, hash).Err(); err != nil {
		return fmt.Errorf("redis delete scoped phone code: %w", err)
	}
	return nil
}

func (s *CodeStore) ConsumeScoped(ctx context.Context, hash string, scope store.PhoneCodeScope) (store.PhoneCode, bool, error) {
	if !scope.Valid() {
		return store.PhoneCode{}, false, nil
	}
	result, err := s.c.Eval(
		ctx,
		consumeScopedCodeScript,
		[]string{codeKey(hash), codeScopeKey(scope)},
		hash,
		store.PhoneCodeVersionCurrent,
	).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return store.PhoneCode{}, false, nil
		}
		return store.PhoneCode{}, false, fmt.Errorf("redis consume scoped phone code: %w", err)
	}
	if result == nil {
		return store.PhoneCode{}, false, nil
	}
	raw, ok := result.(string)
	if !ok {
		return store.PhoneCode{}, false, fmt.Errorf("redis consume scoped phone code: unexpected result %T", result)
	}
	var code store.PhoneCode
	if err := json.Unmarshal([]byte(raw), &code); err != nil {
		return store.PhoneCode{}, false, fmt.Errorf("unmarshal consumed phone code: %w", err)
	}
	if code.Version != store.PhoneCodeVersionCurrent || code.Scope() != scope {
		return store.PhoneCode{}, false, fmt.Errorf("consumed phone code scope mismatch")
	}
	return code, true, nil
}
