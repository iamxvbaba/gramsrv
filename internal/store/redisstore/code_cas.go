package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/store"
)

const getPhoneCodeSnapshotScript = `
local raw = redis.call('GET', KEYS[1])
if not raw then
  return ''
end
local decoded, record = pcall(cjson.decode, raw)
if not decoded or type(record) ~= 'table'
    or tonumber(record.Version or 0) ~= tonumber(ARGV[1])
    or (record.Revision or '') == '' then
  redis.call('DEL', KEYS[1])
  return ''
end
return raw
`

const compareAndUpdatePhoneCodeScript = `
local raw = redis.call('GET', KEYS[1])
if not raw then
  return 0
end
local decoded, record = pcall(cjson.decode, raw)
if not decoded or type(record) ~= 'table'
    or tonumber(record.Version or 0) ~= tonumber(ARGV[1])
    or (record.Revision or '') == '' then
  redis.call('DEL', KEYS[1])
  return 0
end
if (record.Purpose or '') ~= '' or record.Revision ~= ARGV[2] then
  return 0
end
redis.call('SET', KEYS[1], ARGV[3], 'KEEPTTL')
return 1
`

const compareAndDeletePhoneCodeScript = `
local raw = redis.call('GET', KEYS[1])
if not raw then
  return 0
end
local decoded, record = pcall(cjson.decode, raw)
if not decoded or type(record) ~= 'table'
    or tonumber(record.Version or 0) ~= tonumber(ARGV[1])
    or (record.Revision or '') == '' then
  redis.call('DEL', KEYS[1])
  return 0
end
if (record.Purpose or '') ~= '' or record.Revision ~= ARGV[2] then
  return 0
end
redis.call('DEL', KEYS[1])
return 1
`

func (s *CodeStore) GetSnapshot(ctx context.Context, hash string) (store.PhoneCodeSnapshot, bool, error) {
	value, err := s.c.Eval(
		ctx,
		getPhoneCodeSnapshotScript,
		[]string{codeKey(hash)},
		store.PhoneCodeVersionCurrent,
	).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return store.PhoneCodeSnapshot{}, false, nil
		}
		return store.PhoneCodeSnapshot{}, false, fmt.Errorf("redis get phone code snapshot: %w", err)
	}
	raw, ok := value.(string)
	if !ok {
		return store.PhoneCodeSnapshot{}, false, fmt.Errorf("redis get phone code snapshot: unexpected result %T", value)
	}
	if raw == "" {
		return store.PhoneCodeSnapshot{}, false, nil
	}
	var record store.PhoneCode
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return store.PhoneCodeSnapshot{}, false, fmt.Errorf("redis get phone code snapshot decode: %w", err)
	}
	if record.Version != store.PhoneCodeVersionCurrent || record.Revision == "" {
		return store.PhoneCodeSnapshot{}, false, fmt.Errorf("redis get phone code snapshot returned invalid version/revision")
	}
	return store.PhoneCodeSnapshot{Record: record, Revision: record.Revision}, true, nil
}

func (s *CodeStore) CompareAndUpdate(ctx context.Context, hash, expectedRevision string, next store.PhoneCode) (bool, error) {
	if expectedRevision == "" || next.Version != store.PhoneCodeVersionCurrent || next.Purpose != "" {
		return false, nil
	}
	revision, err := store.NewPhoneCodeRevisionToken()
	if err != nil {
		return false, err
	}
	next.Revision = revision
	raw, err := json.Marshal(next)
	if err != nil {
		return false, fmt.Errorf("marshal compare-and-update phone code: %w", err)
	}
	value, err := s.c.Eval(
		ctx,
		compareAndUpdatePhoneCodeScript,
		[]string{codeKey(hash)},
		store.PhoneCodeVersionCurrent,
		expectedRevision,
		string(raw),
	).Result()
	if err != nil {
		return false, fmt.Errorf("redis compare-and-update phone code: %w", err)
	}
	return redisCASApplied(value, "compare-and-update phone code")
}

func (s *CodeStore) CompareAndDelete(ctx context.Context, hash, expectedRevision string) (bool, error) {
	if expectedRevision == "" {
		return false, nil
	}
	value, err := s.c.Eval(
		ctx,
		compareAndDeletePhoneCodeScript,
		[]string{codeKey(hash)},
		store.PhoneCodeVersionCurrent,
		expectedRevision,
	).Result()
	if err != nil {
		return false, fmt.Errorf("redis compare-and-delete phone code: %w", err)
	}
	return redisCASApplied(value, "compare-and-delete phone code")
}

func redisCASApplied(value any, operation string) (bool, error) {
	number, ok := value.(int64)
	if !ok || (number != 0 && number != 1) {
		return false, fmt.Errorf("redis %s: unexpected result %v (%T)", operation, value, value)
	}
	return number == 1, nil
}
