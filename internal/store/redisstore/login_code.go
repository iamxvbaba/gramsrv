package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/store"
)

const verifyLoginCodeScript = `
local raw = redis.call('GET', KEYS[1])
if not raw then
  return {0, ''}
end
local decoded, record = pcall(cjson.decode, raw)
if not decoded or type(record) ~= 'table' then
  redis.call('DEL', KEYS[1])
  return {0, ''}
end
if tonumber(record.Version or 0) ~= tonumber(ARGV[1]) then
  redis.call('DEL', KEYS[1])
  return {0, ''}
end
if record.SignUpVerified == true then
  return {0, ''}
end
local channel = record.Channel or ''
if (record.Purpose or '') ~= '' or (record.Phone or '') ~= ARGV[2]
	or (channel ~= ARGV[6] and channel ~= ARGV[7] and channel ~= ARGV[8])
    or (record.Code or '') == '' or ARGV[3] == '' then
  return {1, raw}
end
if (record.Code or '') ~= ARGV[3] then
  local attempts = tonumber(record.Attempts or 0) + 1
  record.Attempts = attempts
  record.Revision = ARGV[9]
  local max_attempts = tonumber(record.MaxAttempts or 0)
  if not max_attempts or max_attempts <= 0 then
    max_attempts = tonumber(ARGV[5]) or 0
  end
  if max_attempts <= 0 then
    max_attempts = 1
  end
  local updated = cjson.encode(record)
  if attempts >= max_attempts then
    redis.call('DEL', KEYS[1])
  else
    redis.call('SET', KEYS[1], updated, 'KEEPTTL')
  end
  return {1, updated}
end
if ARGV[4] == '1' then
  if tonumber(record.IssuedUserID or '0') ~= 0 then
    return {1, raw}
  end
  record.SignUpVerified = true
	record.Revision = ARGV[9]
  local updated = cjson.encode(record)
  redis.call('SET', KEYS[1], updated, 'KEEPTTL')
  return {2, updated}
end
redis.call('DEL', KEYS[1])
return {2, raw}
`

const verifyScopedCodeScript = `
if redis.call('GET', KEYS[2]) ~= ARGV[1] then
  return {0, ''}
end
local raw = redis.call('GET', KEYS[1])
if not raw then
  redis.call('DEL', KEYS[2])
  return {0, ''}
end
local decoded, record = pcall(cjson.decode, raw)
if not decoded or type(record) ~= 'table' then
  redis.call('DEL', KEYS[1], KEYS[2])
  return {0, ''}
end
if tonumber(record.Version or 0) ~= tonumber(ARGV[2]) then
  redis.call('DEL', KEYS[1], KEYS[2])
  return {0, ''}
end
local encoded_auth_key = ''
if type(record.AuthKeyID) == 'table' then
  encoded_auth_key = cjson.encode(record.AuthKeyID)
end
if (record.Purpose or '') ~= ARGV[6]
    or tonumber(record.UserID or 0) ~= tonumber(ARGV[7])
    or encoded_auth_key ~= ARGV[8]
    or (record.Phone or '') ~= ARGV[9]
    or record.SignUpVerified == true
    or (record.Code or '') == '' then
  redis.call('DEL', KEYS[1], KEYS[2])
  return {0, ''}
end
if ARGV[3] == '' then
  return {1, raw}
end
if (record.Code or '') ~= ARGV[3] then
  local attempts = tonumber(record.Attempts or 0) + 1
  record.Attempts = attempts
  record.Revision = ARGV[5]
  local max_attempts = tonumber(record.MaxAttempts or 0)
  if not max_attempts or max_attempts <= 0 then
    max_attempts = tonumber(ARGV[4]) or 0
  end
  if max_attempts <= 0 then
    max_attempts = 1
  end
  local updated = cjson.encode(record)
  if attempts >= max_attempts then
    redis.call('DEL', KEYS[1], KEYS[2])
  else
    redis.call('SET', KEYS[1], updated, 'KEEPTTL')
  end
  return {1, updated}
end
redis.call('DEL', KEYS[1], KEYS[2])
return {2, raw}
`

const takeLoginCodeScript = `
local raw = redis.call('GET', KEYS[1])
if not raw then
  return ''
end
local decoded, record = pcall(cjson.decode, raw)
if not decoded or type(record) ~= 'table' then
  redis.call('DEL', KEYS[1])
  return ''
end
if tonumber(record.Version or 0) ~= tonumber(ARGV[1]) then
  redis.call('DEL', KEYS[1])
  return ''
end
if record.SignUpVerified == true then
  return ''
end
local channel = record.Channel or ''
if (record.Purpose or '') ~= '' or (record.Phone or '') ~= ARGV[2]
	or (channel ~= ARGV[3] and channel ~= ARGV[4] and channel ~= ARGV[5] and channel ~= ARGV[6]) then
  return ''
end
redis.call('DEL', KEYS[1])
return raw
`

const consumeSignUpVerifiedScript = `
local raw = redis.call('GET', KEYS[1])
if not raw then
  return ''
end
local decoded, record = pcall(cjson.decode, raw)
if not decoded or type(record) ~= 'table' then
  redis.call('DEL', KEYS[1])
  return ''
end
if tonumber(record.Version or 0) ~= tonumber(ARGV[1]) then
  redis.call('DEL', KEYS[1])
  return ''
end
local channel = record.Channel or ''
if (record.Purpose or '') ~= '' or (record.Phone or '') ~= ARGV[2]
	or (channel ~= ARGV[3] and channel ~= ARGV[4] and channel ~= ARGV[5])
    or tonumber(record.IssuedUserID or '0') ~= 0
    or record.SignUpVerified ~= true then
  return ''
end
redis.call('DEL', KEYS[1])
return raw
`

const invalidateLoginCodeScript = `
local raw = redis.call('GET', KEYS[1])
if not raw then
  return ''
end
local decoded, record = pcall(cjson.decode, raw)
if not decoded or type(record) ~= 'table' then
  redis.call('DEL', KEYS[1])
  return ''
end
if tonumber(record.Version or 0) ~= tonumber(ARGV[1]) then
  redis.call('DEL', KEYS[1])
  return ''
end
local channel = record.Channel or ''
if (record.Purpose or '') ~= '' or (record.Phone or '') ~= ARGV[2]
	or (channel ~= ARGV[3] and channel ~= ARGV[4] and channel ~= ARGV[5] and channel ~= ARGV[6]) then
  return ''
end
redis.call('DEL', KEYS[1])
return raw
`

func (s *CodeStore) VerifyLogin(ctx context.Context, hash, phone, code string, keepForSignUp bool, defaultMaxAttempts int) (store.LoginCodeVerifyResult, error) {
	revision, err := store.NewPhoneCodeRevisionToken()
	if err != nil {
		return store.LoginCodeVerifyResult{}, err
	}
	keep := 0
	if keepForSignUp {
		keep = 1
	}
	value, err := s.c.Eval(
		ctx,
		verifyLoginCodeScript,
		[]string{codeKey(hash)},
		store.PhoneCodeVersionCurrent,
		phone,
		code,
		keep,
		defaultMaxAttempts,
		store.PhoneCodeChannelPhone,
		store.PhoneCodeChannelSMS,
		store.PhoneCodeChannelEmailLogin,
		revision,
	).Result()
	if err != nil {
		return store.LoginCodeVerifyResult{}, fmt.Errorf("redis verify login code: %w", err)
	}
	return decodeRedisLoginCodeVerification(value)
}

func (s *CodeStore) VerifyScoped(ctx context.Context, hash string, scope store.PhoneCodeScope, code string, defaultMaxAttempts int) (store.LoginCodeVerifyResult, error) {
	if !scope.Valid() {
		return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyMissing}, nil
	}
	revision, err := store.NewPhoneCodeRevisionToken()
	if err != nil {
		return store.LoginCodeVerifyResult{}, err
	}
	authKeyID, err := json.Marshal(scope.AuthKeyID)
	if err != nil {
		return store.LoginCodeVerifyResult{}, fmt.Errorf("marshal scoped phone code auth key: %w", err)
	}
	value, err := s.c.Eval(
		ctx,
		verifyScopedCodeScript,
		[]string{codeKey(hash), codeScopeKey(scope)},
		hash,
		store.PhoneCodeVersionCurrent,
		code,
		defaultMaxAttempts,
		revision,
		scope.Purpose,
		strconv.FormatInt(scope.UserID, 10),
		string(authKeyID),
		scope.Phone,
	).Result()
	if err != nil {
		return store.LoginCodeVerifyResult{}, fmt.Errorf("redis verify scoped phone code: %w", err)
	}
	result, err := decodeRedisLoginCodeVerification(value)
	if err != nil {
		return store.LoginCodeVerifyResult{}, fmt.Errorf("redis verify scoped phone code: %w", err)
	}
	if result.Status != store.LoginCodeVerifyMissing && result.Record.Scope() != scope {
		return store.LoginCodeVerifyResult{}, fmt.Errorf("redis verify scoped phone code returned a record outside the requested scope")
	}
	return result, nil
}

func (s *CodeStore) ConsumeSignUpVerified(ctx context.Context, hash, phone string) (store.PhoneCode, bool, error) {
	return s.consumeLoginCode(
		ctx,
		consumeSignUpVerifiedScript,
		"consume sign-up verified code",
		hash,
		phone,
		true,
		true,
		store.PhoneCodeChannelPhone,
		store.PhoneCodeChannelSMS,
		store.PhoneCodeChannelEmailLogin,
	)
}

func (s *CodeStore) TakeLoginCode(ctx context.Context, hash, phone string) (store.PhoneCode, bool, error) {
	return s.consumeLoginCode(
		ctx,
		takeLoginCodeScript,
		"take login code",
		hash,
		phone,
		false,
		false,
		store.PhoneCodeChannelPhone,
		store.PhoneCodeChannelSMS,
		store.PhoneCodeChannelEmailLogin,
		store.PhoneCodeChannelEmailSetupRequired,
	)
}

func (s *CodeStore) InvalidateLoginCode(ctx context.Context, hash, phone string) (bool, error) {
	_, found, err := s.consumeLoginCode(
		ctx,
		invalidateLoginCodeScript,
		"invalidate login code",
		hash,
		phone,
		false,
		true,
		store.PhoneCodeChannelPhone,
		store.PhoneCodeChannelSMS,
		store.PhoneCodeChannelEmailLogin,
		store.PhoneCodeChannelEmailSetupRequired,
	)
	return found, err
}

func (s *CodeStore) consumeLoginCode(ctx context.Context, script, operation, hash, phone string, requireVerified, allowVerified bool, channels ...string) (store.PhoneCode, bool, error) {
	args := make([]any, 0, 2+len(channels))
	args = append(args, store.PhoneCodeVersionCurrent, phone)
	for _, channel := range channels {
		args = append(args, channel)
	}
	value, err := s.c.Eval(
		ctx,
		script,
		[]string{codeKey(hash)},
		args...,
	).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return store.PhoneCode{}, false, nil
		}
		return store.PhoneCode{}, false, fmt.Errorf("redis %s: %w", operation, err)
	}
	raw, ok := value.(string)
	if !ok {
		return store.PhoneCode{}, false, fmt.Errorf("redis %s: unexpected result %T", operation, value)
	}
	if raw == "" {
		return store.PhoneCode{}, false, nil
	}
	var record store.PhoneCode
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return store.PhoneCode{}, false, fmt.Errorf("redis %s decode: %w", operation, err)
	}
	if record.Version != store.PhoneCodeVersionCurrent || record.Purpose != "" || record.Phone != phone ||
		!loginCodeChannelAllowed(record.Channel, channels) ||
		(requireVerified && (record.IssuedUserID != 0 || !record.SignUpVerified)) ||
		(!requireVerified && !allowVerified && record.SignUpVerified) {
		return store.PhoneCode{}, false, fmt.Errorf("redis %s returned a record outside the requested login scope", operation)
	}
	return record, true, nil
}

func loginCodeChannelAllowed(channel string, allowed []string) bool {
	for _, item := range allowed {
		if channel == item {
			return true
		}
	}
	return false
}

func decodeRedisLoginCodeVerification(value any) (store.LoginCodeVerifyResult, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) != 2 {
		return store.LoginCodeVerifyResult{}, fmt.Errorf("redis verify login code: unexpected result %T", value)
	}
	statusNumber, ok := items[0].(int64)
	if !ok || statusNumber < int64(store.LoginCodeVerifyMissing) || statusNumber > int64(store.LoginCodeVerifyAccepted) {
		return store.LoginCodeVerifyResult{}, fmt.Errorf("redis verify login code: invalid status %v", items[0])
	}
	result := store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyStatus(statusNumber)}
	if result.Status == store.LoginCodeVerifyMissing {
		return result, nil
	}
	raw, ok := items[1].(string)
	if !ok || raw == "" {
		return store.LoginCodeVerifyResult{}, fmt.Errorf("redis verify login code: status %d has invalid record %T", result.Status, items[1])
	}
	if err := json.Unmarshal([]byte(raw), &result.Record); err != nil {
		return store.LoginCodeVerifyResult{}, fmt.Errorf("redis verify login code decode: %w", err)
	}
	if result.Record.Version != store.PhoneCodeVersionCurrent {
		return store.LoginCodeVerifyResult{}, fmt.Errorf("redis verify login code returned version %d", result.Record.Version)
	}
	return result, nil
}
