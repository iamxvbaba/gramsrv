package redisstore

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	maxEncodedEphemeralMessageBytes = 2 << 20
	ephemeralPushChannel            = "telesrv:ephemeral:push:v1"
)

type EphemeralMessageStore struct {
	c redis.UniversalClient
}

func NewEphemeralMessageStore(c redis.UniversalClient) *EphemeralMessageStore {
	return &EphemeralMessageStore{c: c}
}

func (s *EphemeralMessageStore) PublishEphemeralPush(ctx context.Context, event store.EphemeralPush) error {
	if s == nil || s.c == nil {
		return errors.New("redis ephemeral push broker is not configured")
	}
	if !validEphemeralPush(event) {
		return errors.New("invalid ephemeral push")
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal ephemeral push: %w", err)
	}
	if len(raw) > maxEncodedEphemeralMessageBytes {
		return errors.New("ephemeral push exceeds encoded size limit")
	}
	if err := s.c.Publish(ctx, ephemeralPushChannel, raw).Err(); err != nil {
		return fmt.Errorf("redis publish ephemeral push: %w", err)
	}
	return nil
}

func (s *EphemeralMessageStore) SubscribeEphemeralPushes(ctx context.Context, handle func(context.Context, store.EphemeralPush)) error {
	if s == nil || s.c == nil {
		return errors.New("redis ephemeral push broker is not configured")
	}
	if handle == nil {
		return errors.New("ephemeral push handler is nil")
	}
	pubsub := s.c.Subscribe(ctx, ephemeralPushChannel)
	defer func() { _ = pubsub.Close() }()
	if _, err := pubsub.Receive(ctx); err != nil {
		return fmt.Errorf("redis subscribe ephemeral push: %w", err)
	}
	messages := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item, ok := <-messages:
			if !ok {
				return nil
			}
			if len(item.Payload) > maxEncodedEphemeralMessageBytes {
				continue
			}
			var event store.EphemeralPush
			if strictUnmarshalEphemeral([]byte(item.Payload), &event) != nil || !validEphemeralPush(event) {
				continue
			}
			handle(ctx, event)
		}
	}
}

func validEphemeralPush(event store.EphemeralPush) bool {
	if event.SourceID == "" || event.TargetUserID <= 0 || event.Date <= 0 || event.Message.ID <= 0 ||
		event.Message.Peer.Type != domain.PeerTypeChannel || event.Message.Peer.ID <= 0 || event.Message.ValidateStored() != nil {
		return false
	}
	if event.TargetBusinessAuthKey != ([8]byte{}) &&
		(event.Message.OriginDevice.UserID != event.TargetUserID || event.Message.OriginDevice.BusinessAuthKeyID != event.TargetBusinessAuthKey) {
		return false
	}
	switch event.Kind {
	case store.EphemeralPushNew, store.EphemeralPushEdit:
		return !event.Message.Deleted && event.Callback == nil && event.TargetUserID == event.Message.ReceiverUserID
	case store.EphemeralPushDelete:
		return event.Message.Deleted && event.Callback == nil &&
			(event.TargetUserID == event.Message.SenderUserID || event.TargetUserID == event.Message.ReceiverUserID)
	case store.EphemeralPushCallback:
		return event.Callback != nil && event.Callback.BotUserID == event.TargetUserID &&
			event.Callback.ID != 0 && event.Callback.UserID == event.Message.ReceiverUserID &&
			event.Callback.ChatInstance != 0 && len(event.Callback.Data) <= domain.MaxEphemeralCallbackDataBytes && event.Callback.InlineMessage == nil &&
			event.Callback.MessageID == event.Message.ID && event.Callback.Peer == event.Message.Peer &&
			event.TargetUserID == event.Message.SenderUserID
	default:
		return false
	}
}

func ephemeralPeerTag(peer domain.Peer) string {
	// A shared Redis Cluster hash tag keeps the message and random-id index in
	// the same slot, so the two-key Lua transaction remains cluster-safe.
	return fmt.Sprintf("{ephemeral:%s:%d}", peer.Type, peer.ID)
}

func ephemeralMessageKey(peer domain.Peer, id int) string {
	return fmt.Sprintf("telesrv:%s:message:%d", ephemeralPeerTag(peer), id)
}

func ephemeralRandomKey(message domain.EphemeralMessage) string {
	return fmt.Sprintf("telesrv:%s:random:%d:%d:%d", ephemeralPeerTag(message.Peer),
		message.SenderUserID, message.ReceiverUserID, message.RandomID)
}

func ephemeralCallbackActionKey(queryID int64) string {
	return fmt.Sprintf("telesrv:ephemeral:callback_action:%d", queryID)
}

func (s *EphemeralMessageStore) PutEphemeralCallbackAction(ctx context.Context, action domain.EphemeralCallbackAction) (bool, error) {
	if s == nil || s.c == nil || action.QueryID == 0 || action.BotUserID <= 0 || action.UserID <= 0 ||
		action.Peer.Type != domain.PeerTypeChannel || action.Peer.ID <= 0 || action.MessageID <= 0 ||
		action.Device.UserID != action.UserID || action.Device.BusinessAuthKeyID == ([8]byte{}) || action.CreatedAt.IsZero() ||
		!action.ExpiresAt.After(action.CreatedAt) || action.ExpiresAt.Sub(action.CreatedAt) > domain.EphemeralReplyWindow {
		return false, domain.ErrEphemeralInvalid
	}
	ttl := time.Until(action.ExpiresAt)
	if ttl <= 0 || ttl > domain.EphemeralReplyWindow {
		return false, domain.ErrEphemeralReplyExpired
	}
	raw, err := json.Marshal(action)
	if err != nil {
		return false, fmt.Errorf("marshal ephemeral callback action: %w", err)
	}
	created, err := s.c.SetNX(ctx, ephemeralCallbackActionKey(action.QueryID), raw, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("redis put ephemeral callback action: %w", err)
	}
	return created, nil
}

func (s *EphemeralMessageStore) GetEphemeralCallbackAction(ctx context.Context, botUserID, queryID int64, now time.Time) (domain.EphemeralCallbackAction, bool, error) {
	if s == nil || s.c == nil || botUserID <= 0 || queryID == 0 {
		return domain.EphemeralCallbackAction{}, false, nil
	}
	key := ephemeralCallbackActionKey(queryID)
	raw, err := s.c.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return domain.EphemeralCallbackAction{}, false, nil
	}
	if err != nil {
		return domain.EphemeralCallbackAction{}, false, fmt.Errorf("redis get ephemeral callback action: %w", err)
	}
	var action domain.EphemeralCallbackAction
	if strictUnmarshalEphemeral(raw, &action) != nil || action.QueryID != queryID || action.BotUserID != botUserID ||
		action.UserID <= 0 || action.Peer.Type != domain.PeerTypeChannel || action.Peer.ID <= 0 || action.MessageID <= 0 ||
		action.Device.UserID != action.UserID || !now.Before(action.ExpiresAt) {
		_ = s.c.Del(ctx, key).Err()
		return domain.EphemeralCallbackAction{}, false, nil
	}
	return action, true, nil
}

var createEphemeralMessageScript = redis.NewScript(`
local index = redis.call('GET', KEYS[2])
if index then
  local separator = string.find(index, '\n', 1, true)
  if not separator then
    return {4, ''}
  end
  local target = string.sub(index, 1, separator - 1)
  local payload_hash = string.sub(index, separator + 1)
  local existing = redis.call('GET', target)
  if existing then
    if payload_hash ~= ARGV[2] then
      return {2, ''}
    end
    return {1, existing}
  end
  redis.call('DEL', KEYS[2])
end
if redis.call('EXISTS', KEYS[1]) ~= 0 then
  return {3, ''}
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
redis.call('SET', KEYS[2], KEYS[1] .. '\n' .. ARGV[2], 'PX', ARGV[3])
return {0, ARGV[1]}
`)

func (s *EphemeralMessageStore) CreateEphemeralMessage(ctx context.Context, message domain.EphemeralMessage) (domain.EphemeralMessage, bool, error) {
	if s == nil || s.c == nil {
		return domain.EphemeralMessage{}, false, errors.New("redis ephemeral store is not configured")
	}
	now := message.CreatedAt
	if err := message.ValidateForCreate(now); err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	ttl := message.ExpiresAt.Sub(now)
	if ttl <= 0 || ttl > domain.EphemeralMessageRetention {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralInvalid
	}
	raw, err := marshalEphemeralMessage(message)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	value, err := createEphemeralMessageScript.Run(ctx, s.c, []string{
		ephemeralMessageKey(message.Peer, message.ID), ephemeralRandomKey(message),
	}, raw, hex.EncodeToString(message.PayloadHash[:]), ttl.Milliseconds()).Result()
	if err != nil {
		return domain.EphemeralMessage{}, false, fmt.Errorf("redis create ephemeral message: %w", err)
	}
	status, encoded, err := decodeEphemeralScriptResult(value)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	switch status {
	case 0, 1:
		stored, err := unmarshalEphemeralMessage(encoded)
		return stored, status == 0, err
	case 2:
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralRandomIDConflict
	case 3:
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralIDCollision
	default:
		return domain.EphemeralMessage{}, false, fmt.Errorf("redis ephemeral create index is corrupt")
	}
}

func (s *EphemeralMessageStore) GetEphemeralMessage(ctx context.Context, peer domain.Peer, id int, now time.Time) (domain.EphemeralMessage, bool, error) {
	if s == nil || s.c == nil || peer.ID <= 0 || id <= 0 {
		return domain.EphemeralMessage{}, false, nil
	}
	raw, err := s.c.Get(ctx, ephemeralMessageKey(peer, id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return domain.EphemeralMessage{}, false, nil
	}
	if err != nil {
		return domain.EphemeralMessage{}, false, fmt.Errorf("redis get ephemeral message: %w", err)
	}
	message, err := unmarshalEphemeralMessage(raw)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	if message.Peer != peer || message.ID != id {
		return domain.EphemeralMessage{}, false, fmt.Errorf("redis ephemeral message identity mismatch")
	}
	if message.Expired(now) {
		return domain.EphemeralMessage{}, false, nil
	}
	return message, true, nil
}

var editEphemeralMessageScript = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then
  return {0, ''}
end
local decoded, record = pcall(cjson.decode, raw)
if not decoded or type(record) ~= 'table' or tonumber(record.Version or 0) <= 0 then
  return {4, ''}
end
if record.Deleted == true then
  return {2, raw}
end
if tonumber(record.Version) ~= tonumber(ARGV[1]) then
  return {3, raw}
end
if redis.call('PTTL', KEYS[1]) <= 0 then
  return {4, ''}
end
redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL')
return {1, ARGV[2]}
`)

func (s *EphemeralMessageStore) EditEphemeralMessage(ctx context.Context, peer domain.Peer, id int, expectedVersion uint64, content domain.EphemeralContent, editDate int, now time.Time) (domain.EphemeralMessage, error) {
	current, found, err := s.GetEphemeralMessage(ctx, peer, id, now)
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	if !found {
		return domain.EphemeralMessage{}, domain.ErrEphemeralNotFound
	}
	if current.Deleted {
		return domain.EphemeralMessage{}, domain.ErrEphemeralDeleted
	}
	if expectedVersion == 0 || current.Version != expectedVersion {
		return domain.EphemeralMessage{}, domain.ErrEphemeralVersionConflict
	}
	if domain.ValidateEphemeralContent(content) != nil {
		return domain.EphemeralMessage{}, domain.ErrEphemeralInvalid
	}
	current.Content = content
	current.EditDate = editDate
	current.Version++
	replacement, err := marshalEphemeralMessage(current)
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	value, err := editEphemeralMessageScript.Run(ctx, s.c, []string{ephemeralMessageKey(peer, id)}, expectedVersion, replacement).Result()
	if err != nil {
		return domain.EphemeralMessage{}, fmt.Errorf("redis edit ephemeral message: %w", err)
	}
	status, encoded, err := decodeEphemeralScriptResult(value)
	if err != nil {
		return domain.EphemeralMessage{}, err
	}
	switch status {
	case 1:
		return unmarshalEphemeralMessage(encoded)
	case 0:
		return domain.EphemeralMessage{}, domain.ErrEphemeralNotFound
	case 2:
		return domain.EphemeralMessage{}, domain.ErrEphemeralDeleted
	case 3:
		return domain.EphemeralMessage{}, domain.ErrEphemeralVersionConflict
	default:
		return domain.EphemeralMessage{}, fmt.Errorf("redis ephemeral edit record is corrupt")
	}
}

var deleteEphemeralMessageScript = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then
  return {0, ''}
end
local decoded, record = pcall(cjson.decode, raw)
if not decoded or type(record) ~= 'table' or tonumber(record.Version or 0) <= 0 then
  return {4, ''}
end
if record.Deleted == true then
  return {2, raw}
end
if tonumber(record.Version) ~= tonumber(ARGV[1]) then
  return {3, raw}
end
if redis.call('PTTL', KEYS[1]) <= 0 then
  return {4, ''}
end
redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL')
return {1, ARGV[2]}
`)

func (s *EphemeralMessageStore) DeleteEphemeralMessage(ctx context.Context, peer domain.Peer, id int, expectedVersion uint64, now time.Time) (domain.EphemeralMessage, bool, error) {
	current, found, err := s.GetEphemeralMessage(ctx, peer, id, now)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	if !found {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralNotFound
	}
	if current.Deleted {
		return current, false, nil
	}
	if expectedVersion == 0 || current.Version != expectedVersion {
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralVersionConflict
	}
	current.Deleted = true
	current.Version++
	current.Content = domain.EphemeralContent{}
	replacement, err := marshalEphemeralMessage(current)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	value, err := deleteEphemeralMessageScript.Run(ctx, s.c, []string{ephemeralMessageKey(peer, id)}, expectedVersion, replacement).Result()
	if err != nil {
		return domain.EphemeralMessage{}, false, fmt.Errorf("redis delete ephemeral message: %w", err)
	}
	status, encoded, err := decodeEphemeralScriptResult(value)
	if err != nil {
		return domain.EphemeralMessage{}, false, err
	}
	switch status {
	case 1, 2:
		message, err := unmarshalEphemeralMessage(encoded)
		return message, status == 1, err
	case 0:
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralNotFound
	case 3:
		return domain.EphemeralMessage{}, false, domain.ErrEphemeralVersionConflict
	default:
		return domain.EphemeralMessage{}, false, fmt.Errorf("redis ephemeral delete record is corrupt")
	}
}

func (*EphemeralMessageStore) PruneExpiredEphemeralMessages(context.Context, time.Time, int) (int, error) {
	// Redis key expiry is the authoritative O(1) cleanup path; no key scan is
	// permitted here because SCAN cost would grow with total ephemeral volume.
	return 0, nil
}

func marshalEphemeralMessage(message domain.EphemeralMessage) ([]byte, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal ephemeral message: %w", err)
	}
	if len(raw) == 0 || len(raw) > maxEncodedEphemeralMessageBytes {
		return nil, domain.ErrEphemeralInvalid
	}
	return raw, nil
}

func unmarshalEphemeralMessage(raw []byte) (domain.EphemeralMessage, error) {
	if len(raw) == 0 || len(raw) > maxEncodedEphemeralMessageBytes {
		return domain.EphemeralMessage{}, fmt.Errorf("redis ephemeral message has invalid encoded size")
	}
	var message domain.EphemeralMessage
	if err := strictUnmarshalEphemeral(raw, &message); err != nil {
		return domain.EphemeralMessage{}, fmt.Errorf("decode redis ephemeral message: %w", err)
	}
	if message.ValidateStored() != nil {
		return domain.EphemeralMessage{}, fmt.Errorf("redis ephemeral message violates stored invariants")
	}
	return message, nil
}

func strictUnmarshalEphemeral(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing ephemeral JSON")
	}
	return nil
}

func decodeEphemeralScriptResult(value any) (int64, []byte, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) != 2 {
		return 0, nil, fmt.Errorf("redis ephemeral script returned %T", value)
	}
	status, ok := items[0].(int64)
	if !ok {
		return 0, nil, fmt.Errorf("redis ephemeral script returned invalid status %T", items[0])
	}
	var raw []byte
	switch value := items[1].(type) {
	case string:
		raw = []byte(value)
	case []byte:
		raw = append([]byte(nil), value...)
	case nil:
	default:
		return 0, nil, fmt.Errorf("redis ephemeral script returned invalid payload %T", items[1])
	}
	return status, raw, nil
}
