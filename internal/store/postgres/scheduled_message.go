package postgres

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

const maxScheduledMessagePage = 100

// scheduledRetryBackoffSeconds 是后台派发投递失败后的最小重试间隔：投递失败的
// pending 行（last_error 非空）在该退避窗口内不再被 ClaimDueScheduledMessages
// 认领，避免对端注销等永久失败的到期消息每秒被反复 claim+send。
const scheduledRetryBackoffSeconds = 30

func (s *MessageStore) CreateScheduledMessage(ctx context.Context, req domain.ScheduleMessageRequest) (domain.ScheduledMessage, error) {
	if req.OwnerUserID == 0 || req.Peer.ID == 0 || req.ScheduleDate <= 0 {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: invalid request")
	}
	if req.Peer.Type != domain.PeerTypeUser && req.Peer.Type != domain.PeerTypeChannel {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: invalid peer")
	}
	if req.Message == "" && req.Media.IsZero() && req.RichMessage.IsZero() {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: empty message")
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	entities, err := encodeMessageEntities(req.Entities)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	media, err := encodeMessageMedia(req.Media)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	richMessage, err := encodeRichMessage(req.RichMessage)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	meta, err := messageMetadataParamsFrom(req.Silent, req.NoForwards, req.ReplyTo, req.Forward)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("begin create scheduled message: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := lockUsersForUpdate(ctx, tx, req.OwnerUserID); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("lock scheduled owner: %w", err)
	}
	if req.RandomID != 0 {
		existing, ok, err := getPendingScheduledByRandomID(ctx, tx, req.OwnerUserID, req.RandomID)
		if err != nil {
			return domain.ScheduledMessage{}, err
		}
		if ok {
			if err := tx.Commit(ctx); err != nil {
				return domain.ScheduledMessage{}, fmt.Errorf("commit duplicate scheduled message: %w", err)
			}
			committed = true
			return existing, nil
		}
	}
	nextID, err := nextScheduledMessageID(ctx, tx, req.OwnerUserID)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	sendAsType, sendAsID := "", int64(0)
	if req.SendAs != nil {
		sendAsType = string(req.SendAs.Type)
		sendAsID = req.SendAs.ID
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO scheduled_messages (
  owner_user_id, scheduled_id, peer_type, peer_id, random_id, message_date,
  body, entities, media, rich_message, silent, noforwards,
  reply_to_msg_id, reply_to_peer_type, reply_to_peer_id, reply_to_top_id,
  quote_text, quote_entities, quote_offset,
  fwd_from_peer_type, fwd_from_peer_id, fwd_from_name, fwd_date,
  send_as_peer_type, send_as_peer_id,
  schedule_date, schedule_repeat_period, state, created_at, updated_at
) VALUES (
  $1, $2, $3, $4, $5, $6,
  $7, $8::jsonb, $9::jsonb, $10::jsonb, $11, $12,
  $13, $14, $15, $16,
  $17, $18::jsonb, $19,
  $20, $21, $22, $23,
  $24, $25,
  $26, $27, 'pending', $28, $28
)`, req.OwnerUserID, nextID, string(req.Peer.Type), req.Peer.ID, req.RandomID, req.Date,
		req.Message, entities, media, richMessage, req.Silent, req.NoForwards,
		meta.ReplyToMsgID, meta.ReplyToPeerType, meta.ReplyToPeerID, meta.ReplyToTopID,
		meta.QuoteText, meta.QuoteEntitiesJSON, meta.QuoteOffset,
		meta.FwdFromPeerType, meta.FwdFromPeerID, meta.FwdFromName, meta.FwdDate,
		sendAsType, sendAsID,
		req.ScheduleDate, req.ScheduleRepeatPeriod, req.Date); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("insert scheduled message: %w", err)
	}
	if err := setHasScheduledTx(ctx, tx, req.OwnerUserID, req.Peer, true); err != nil {
		return domain.ScheduledMessage{}, err
	}
	msg, ok, err := getScheduledByID(ctx, tx, req.OwnerUserID, nextID)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if !ok {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: inserted row missing")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("commit create scheduled message: %w", err)
	}
	committed = true
	return msg, nil
}

func (s *MessageStore) ListScheduledMessages(ctx context.Context, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	return listScheduledMessages(ctx, s.db, filter, false)
}

func (s *MessageStore) EditScheduledMessage(ctx context.Context, req domain.EditScheduledMessageRequest) (domain.ScheduledMessage, error) {
	if req.OwnerUserID == 0 || req.Peer.ID == 0 || req.ID <= 0 || req.ScheduleDate <= 0 {
		return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
	}
	if req.Peer.Type != domain.PeerTypeUser && req.Peer.Type != domain.PeerTypeChannel {
		return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ScheduledMessage{}, fmt.Errorf("edit scheduled message: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("begin edit scheduled message: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := lockUsersForUpdate(ctx, tx, req.OwnerUserID); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("lock scheduled edit owner: %w", err)
	}
	current, ok, err := getPendingScheduledForEdit(ctx, tx, req.OwnerUserID, req.Peer, req.ID)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	if !ok {
		return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
	}
	message := current.Message
	entities := append([]domain.MessageEntity(nil), current.Entities...)
	richMessage := current.RichMessage
	if req.SetMessage {
		message = req.Message
		entities = append([]domain.MessageEntity(nil), req.Entities...)
	}
	if req.SetRichMessage {
		richMessage = req.RichMessage
	}
	if message == "" && current.Media.IsZero() && richMessage.IsZero() {
		return domain.ScheduledMessage{}, domain.ErrMessageEmpty
	}
	encodedEntities, err := encodeMessageEntities(entities)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	encodedRichMessage, err := encodeRichMessage(richMessage)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	row := tx.QueryRow(ctx, `
UPDATE scheduled_messages
SET body = $5,
    entities = $6::jsonb,
    rich_message = $7::jsonb,
    schedule_date = $8,
    updated_at = $9
WHERE owner_user_id = $1
  AND peer_type = $2
  AND peer_id = $3
  AND scheduled_id = $4
  AND state = 'pending'
RETURNING `+scheduledMessageSelectColumns(), req.OwnerUserID, string(req.Peer.Type), req.Peer.ID, req.ID, message, encodedEntities, encodedRichMessage, req.ScheduleDate, req.Date)
	msg, err := scanScheduledMessage(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
		}
		return domain.ScheduledMessage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("commit edit scheduled message: %w", err)
	}
	committed = true
	return msg, nil
}

func (s *MessageStore) GetScheduledMessages(ctx context.Context, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	return listScheduledMessages(ctx, s.db, filter, true)
}

func (s *MessageStore) DeleteScheduledMessages(ctx context.Context, filter domain.ScheduledMessageFilter, date int) ([]domain.ScheduledMessage, error) {
	if filter.OwnerUserID == 0 || filter.Peer.ID == 0 || len(filter.IDs) == 0 {
		return nil, nil
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return nil, fmt.Errorf("delete scheduled messages: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin delete scheduled messages: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := lockUsersForUpdate(ctx, tx, filter.OwnerUserID); err != nil {
		return nil, fmt.Errorf("lock scheduled delete owner: %w", err)
	}
	rows, err := tx.Query(ctx, `
UPDATE scheduled_messages
SET state = 'deleted',
    updated_at = $5
WHERE owner_user_id = $1
  AND peer_type = $2
  AND peer_id = $3
  AND scheduled_id = ANY($4::int[])
  AND state IN ('pending', 'dispatching')
RETURNING `+scheduledMessageSelectColumns(), filter.OwnerUserID, string(filter.Peer.Type), filter.Peer.ID, intSliceToInt32(filter.IDs), date)
	if err != nil {
		return nil, fmt.Errorf("delete scheduled messages: %w", err)
	}
	deleted, err := scanScheduledMessages(rows)
	if err != nil {
		return nil, err
	}
	if err := refreshHasScheduledTx(ctx, tx, filter.OwnerUserID, filter.Peer); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit delete scheduled messages: %w", err)
	}
	committed = true
	return deleted, nil
}

func (s *MessageStore) ClaimScheduledMessages(ctx context.Context, claim domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error) {
	if claim.OwnerUserID == 0 || claim.Peer.ID == 0 || len(claim.IDs) == 0 {
		return nil, nil
	}
	if claim.Now == 0 {
		claim.Now = int(time.Now().Unix())
	}
	if claim.LeaseUntil == 0 {
		claim.LeaseUntil = claim.Now + 60
	}
	rows, err := s.db.Query(ctx, `
UPDATE scheduled_messages
SET state = 'dispatching',
    lease_until = $6,
    updated_at = $5,
    last_error = ''
WHERE owner_user_id = $1
  AND peer_type = $2
  AND peer_id = $3
  AND scheduled_id = ANY($4::int[])
  AND (state = 'pending' OR (state = 'dispatching' AND lease_until <= $5))
RETURNING `+scheduledMessageSelectColumns(), claim.OwnerUserID, string(claim.Peer.Type), claim.Peer.ID, intSliceToInt32(claim.IDs), claim.Now, claim.LeaseUntil)
	if err != nil {
		return nil, fmt.Errorf("claim scheduled messages: %w", err)
	}
	return scanScheduledMessages(rows)
}

func (s *MessageStore) ClaimDueScheduledMessages(ctx context.Context, now, limit, leaseSeconds int) ([]domain.ScheduledMessage, error) {
	if now <= 0 || limit <= 0 {
		return nil, nil
	}
	if limit > maxScheduledMessagePage {
		limit = maxScheduledMessagePage
	}
	if leaseSeconds <= 0 {
		leaseSeconds = 60
	}
	// retryFloor 让上一次投递失败（last_error 非空）的 pending 行至少退避
	// scheduledRetryBackoffSeconds 秒再被认领；同时回收 lease 已过期却仍卡在
	// 'dispatching' 的行——派发器在 claim 之后、MarkScheduledMessageSent 之前
	// 崩溃/部署/OOM 会留下这种行，旧实现只认领 'pending' 导致该定时消息永久不
	// 投递且 has_scheduled 永远为真。重投由 random_id 幂等兜底（命中 Duplicate
	// 仍会 MarkScheduledMessageSent 清状态）。
	retryFloor := now - scheduledRetryBackoffSeconds
	rows, err := s.db.Query(ctx, `
WITH due AS (
  SELECT sm.owner_user_id, sm.scheduled_id
  FROM scheduled_messages sm
  WHERE sm.schedule_date <= $1
    AND (
      (sm.state = 'pending' AND (sm.last_error = '' OR sm.updated_at <= $4))
      OR (sm.state = 'dispatching' AND sm.lease_until <= $1)
    )
  ORDER BY sm.schedule_date ASC, sm.owner_user_id ASC, sm.scheduled_id ASC
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
UPDATE scheduled_messages sm
SET state = 'dispatching',
    lease_until = $1 + $3,
    updated_at = $1,
    last_error = ''
FROM due
WHERE sm.owner_user_id = due.owner_user_id
  AND sm.scheduled_id = due.scheduled_id
RETURNING `+scheduledMessageSelectColumnsFor("sm"), now, limit, leaseSeconds, retryFloor)
	if err != nil {
		return nil, fmt.Errorf("claim due scheduled messages: %w", err)
	}
	return scanScheduledMessages(rows)
}

func (s *MessageStore) MarkScheduledMessageSent(ctx context.Context, ownerUserID int64, id, sentMessageID, date int) error {
	if ownerUserID == 0 || id <= 0 {
		return nil
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return fmt.Errorf("mark scheduled sent: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin mark scheduled sent: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := lockUsersForUpdate(ctx, tx, ownerUserID); err != nil {
		return fmt.Errorf("lock scheduled sent owner: %w", err)
	}
	row := tx.QueryRow(ctx, `
UPDATE scheduled_messages
SET state = 'sent',
    sent_message_id = $3,
    lease_until = 0,
    updated_at = $4
WHERE owner_user_id = $1
  AND scheduled_id = $2
RETURNING peer_type, peer_id`, ownerUserID, id, sentMessageID, date)
	var peerType string
	var peerID int64
	if err := row.Scan(&peerType, &peerID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return fmt.Errorf("mark scheduled sent: %w", err)
	}
	if err := refreshHasScheduledTx(ctx, tx, ownerUserID, domain.Peer{Type: domain.PeerType(peerType), ID: peerID}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit mark scheduled sent: %w", err)
	}
	committed = true
	return nil
}

func (s *MessageStore) ReleaseScheduledMessage(ctx context.Context, ownerUserID int64, id int, errText string) error {
	if ownerUserID == 0 || id <= 0 {
		return nil
	}
	_, err := s.db.Exec(ctx, `
UPDATE scheduled_messages
SET state = 'pending',
    lease_until = 0,
    last_error = $3,
    updated_at = EXTRACT(EPOCH FROM now())::int
WHERE owner_user_id = $1
  AND scheduled_id = $2
  AND state = 'dispatching'`, ownerUserID, id, errText)
	if err != nil {
		return fmt.Errorf("release scheduled message: %w", err)
	}
	return nil
}

func (s *MessageStore) HasScheduledMessages(ctx context.Context, ownerUserID int64, peer domain.Peer) (bool, error) {
	if ownerUserID == 0 || peer.ID == 0 {
		return false, nil
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM scheduled_messages
  WHERE owner_user_id = $1
    AND peer_type = $2
    AND peer_id = $3
    AND state IN ('pending', 'dispatching')
  LIMIT 1
)::boolean`, ownerUserID, string(peer.Type), peer.ID).Scan(&exists); err != nil {
		return false, fmt.Errorf("has scheduled messages: %w", err)
	}
	return exists, nil
}

func listScheduledMessages(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, filter domain.ScheduledMessageFilter, exact bool) (domain.ScheduledMessageList, error) {
	if filter.OwnerUserID == 0 || filter.Peer.ID == 0 {
		return domain.ScheduledMessageList{}, nil
	}
	limit := filter.Limit
	if limit <= 0 || limit > maxScheduledMessagePage {
		limit = maxScheduledMessagePage
	}
	var (
		rows pgx.Rows
		err  error
	)
	if exact {
		if len(filter.IDs) == 0 {
			return domain.ScheduledMessageList{}, nil
		}
		rows, err = db.Query(ctx, `
SELECT `+scheduledMessageSelectColumns()+`
FROM scheduled_messages
WHERE owner_user_id = $1
  AND peer_type = $2
  AND peer_id = $3
  AND scheduled_id = ANY($4::int[])
  AND state IN ('pending', 'dispatching')
ORDER BY schedule_date ASC, scheduled_id ASC
LIMIT $5`, filter.OwnerUserID, string(filter.Peer.Type), filter.Peer.ID, intSliceToInt32(filter.IDs), limit)
	} else {
		rows, err = db.Query(ctx, `
SELECT `+scheduledMessageSelectColumns()+`
FROM scheduled_messages
WHERE owner_user_id = $1
  AND peer_type = $2
  AND peer_id = $3
  AND state IN ('pending', 'dispatching')
ORDER BY schedule_date ASC, scheduled_id ASC
LIMIT $4`, filter.OwnerUserID, string(filter.Peer.Type), filter.Peer.ID, limit)
	}
	if err != nil {
		return domain.ScheduledMessageList{}, fmt.Errorf("list scheduled messages: %w", err)
	}
	msgs, err := scanScheduledMessages(rows)
	if err != nil {
		return domain.ScheduledMessageList{}, err
	}
	out := domain.ScheduledMessageList{Messages: msgs, Count: len(msgs), Hash: scheduledMessageListHash(msgs)}
	if filter.Hash != 0 && filter.Hash == out.Hash {
		out.Messages = nil
		out.Count = 0
	}
	return out, nil
}

func scheduledMessageSelectColumns() string {
	return `owner_user_id, scheduled_id, peer_type, peer_id, random_id, message_date,
body, entities::text, media::text, rich_message::text, silent, noforwards,
reply_to_msg_id, reply_to_peer_type, reply_to_peer_id, reply_to_top_id,
quote_text, quote_entities::text, quote_offset,
fwd_from_peer_type, fwd_from_peer_id, fwd_from_name, fwd_date,
send_as_peer_type, send_as_peer_id,
schedule_date, schedule_repeat_period, state, sent_message_id, created_at, updated_at`
}

func scheduledMessageSelectColumnsFor(alias string) string {
	if alias == "" {
		return scheduledMessageSelectColumns()
	}
	prefix := alias + "."
	return prefix + `owner_user_id, ` + prefix + `scheduled_id, ` + prefix + `peer_type, ` + prefix + `peer_id, ` + prefix + `random_id, ` + prefix + `message_date,
` + prefix + `body, ` + prefix + `entities::text, ` + prefix + `media::text, ` + prefix + `rich_message::text, ` + prefix + `silent, ` + prefix + `noforwards,
` + prefix + `reply_to_msg_id, ` + prefix + `reply_to_peer_type, ` + prefix + `reply_to_peer_id, ` + prefix + `reply_to_top_id,
` + prefix + `quote_text, ` + prefix + `quote_entities::text, ` + prefix + `quote_offset,
` + prefix + `fwd_from_peer_type, ` + prefix + `fwd_from_peer_id, ` + prefix + `fwd_from_name, ` + prefix + `fwd_date,
` + prefix + `send_as_peer_type, ` + prefix + `send_as_peer_id,
` + prefix + `schedule_date, ` + prefix + `schedule_repeat_period, ` + prefix + `state, ` + prefix + `sent_message_id, ` + prefix + `created_at, ` + prefix + `updated_at`
}

func scanScheduledMessages(rows pgx.Rows) ([]domain.ScheduledMessage, error) {
	defer rows.Close()
	out := make([]domain.ScheduledMessage, 0)
	for rows.Next() {
		msg, err := scanScheduledMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scheduled messages: %w", err)
	}
	return out, nil
}

func scanScheduledMessage(scanner interface{ Scan(...any) error }) (domain.ScheduledMessage, error) {
	var (
		msg                  domain.ScheduledMessage
		peerType             string
		entitiesJSON         string
		mediaJSON            string
		richMessageJSON      string
		replyToMsgID         int32
		replyToPeerType      string
		replyToPeerID        int64
		replyToTopID         int32
		quoteText            string
		quoteEntitiesJSON    string
		quoteOffset          int32
		fwdFromPeerType      string
		fwdFromPeerID        int64
		fwdFromName          string
		fwdDate              int32
		sendAsPeerType       string
		sendAsPeerID         int64
		scheduleRepeatPeriod int32
	)
	if err := scanner.Scan(
		&msg.OwnerUserID, &msg.ID, &peerType, &msg.Peer.ID, &msg.RandomID, &msg.CreatedAt,
		&msg.Message, &entitiesJSON, &mediaJSON, &richMessageJSON, &msg.Silent, &msg.NoForwards,
		&replyToMsgID, &replyToPeerType, &replyToPeerID, &replyToTopID,
		&quoteText, &quoteEntitiesJSON, &quoteOffset,
		&fwdFromPeerType, &fwdFromPeerID, &fwdFromName, &fwdDate,
		&sendAsPeerType, &sendAsPeerID,
		&msg.ScheduleDate, &scheduleRepeatPeriod, &msg.State, &msg.SentMessageID, &msg.CreatedAt, &msg.UpdatedAt,
	); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("scan scheduled message: %w", err)
	}
	msg.Peer.Type = domain.PeerType(peerType)
	msg.ScheduleRepeatPeriod = int(scheduleRepeatPeriod)
	entities, err := decodeMessageEntities(entitiesJSON)
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("decode scheduled entities: %w", err)
	}
	msg.Entities = entities
	media, err := decodeMessageMedia(mediaJSON)
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("decode scheduled media: %w", err)
	}
	msg.Media = media
	richMessage, err := decodeRichMessage(richMessageJSON)
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("decode scheduled rich message: %w", err)
	}
	msg.RichMessage = richMessage
	// scheduled_messages 不存 saved_from：到点投递经 SendPrivateText 实时
	// 重算 saved 语义（self-chat 直发归 self），fwd saved 维度恒空。
	_, _, reply, forward, err := messageMetadataFromFields(
		msg.Silent, msg.NoForwards, replyToMsgID, replyToPeerType, replyToPeerID, replyToTopID,
		0, // scheduled_messages 不持久化 story 回复（定时回复 story 是边角，恒 0）
		quoteText, quoteEntitiesJSON, quoteOffset,
		fwdFromPeerType, fwdFromPeerID, fwdFromName, fwdDate,
		"", 0, 0,
	)
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("decode scheduled metadata: %w", err)
	}
	msg.ReplyTo = reply
	msg.Forward = forward
	if sendAsPeerType != "" && sendAsPeerID != 0 {
		msg.SendAs = &domain.Peer{Type: domain.PeerType(sendAsPeerType), ID: sendAsPeerID}
	}
	return msg, nil
}

func getPendingScheduledByRandomID(ctx context.Context, db pgx.Tx, ownerUserID, randomID int64) (domain.ScheduledMessage, bool, error) {
	row := db.QueryRow(ctx, `
SELECT `+scheduledMessageSelectColumns()+`
FROM scheduled_messages
WHERE owner_user_id = $1
  AND random_id = $2
  AND random_id <> 0
  AND state IN ('pending', 'dispatching')
ORDER BY scheduled_id ASC
LIMIT 1`, ownerUserID, randomID)
	msg, err := scanScheduledMessage(row)
	if err == nil {
		return msg, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ScheduledMessage{}, false, nil
	}
	return domain.ScheduledMessage{}, false, err
}

func getScheduledByID(ctx context.Context, db pgx.Tx, ownerUserID int64, id int) (domain.ScheduledMessage, bool, error) {
	row := db.QueryRow(ctx, `
SELECT `+scheduledMessageSelectColumns()+`
FROM scheduled_messages
WHERE owner_user_id = $1
  AND scheduled_id = $2
LIMIT 1`, ownerUserID, id)
	msg, err := scanScheduledMessage(row)
	if err == nil {
		return msg, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ScheduledMessage{}, false, nil
	}
	return domain.ScheduledMessage{}, false, err
}

func getPendingScheduledForEdit(ctx context.Context, db pgx.Tx, ownerUserID int64, peer domain.Peer, id int) (domain.ScheduledMessage, bool, error) {
	row := db.QueryRow(ctx, `
SELECT `+scheduledMessageSelectColumns()+`
FROM scheduled_messages
WHERE owner_user_id = $1
  AND peer_type = $2
  AND peer_id = $3
  AND scheduled_id = $4
  AND state = 'pending'
FOR UPDATE`, ownerUserID, string(peer.Type), peer.ID, id)
	msg, err := scanScheduledMessage(row)
	if err == nil {
		return msg, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ScheduledMessage{}, false, nil
	}
	return domain.ScheduledMessage{}, false, err
}

func nextScheduledMessageID(ctx context.Context, db pgx.Tx, ownerUserID int64) (int, error) {
	var id int
	if err := db.QueryRow(ctx, `
SELECT COALESCE(MAX(scheduled_id), 0)::int + 1
FROM scheduled_messages
WHERE owner_user_id = $1`, ownerUserID).Scan(&id); err != nil {
		return 0, fmt.Errorf("next scheduled message id: %w", err)
	}
	if id <= 0 || id > domain.MaxMessageBoxID {
		return 0, domain.ErrMessageIDInvalid
	}
	return id, nil
}

func setHasScheduledTx(ctx context.Context, db pgx.Tx, ownerUserID int64, peer domain.Peer, has bool) error {
	switch peer.Type {
	case domain.PeerTypeUser:
		_, err := db.Exec(ctx, `
INSERT INTO dialogs (user_id, peer_type, peer_id, has_scheduled)
VALUES ($1, 'user', $2, $3)
ON CONFLICT (user_id, peer_type, peer_id) DO UPDATE SET
  has_scheduled = EXCLUDED.has_scheduled,
  updated_at = now()`, ownerUserID, peer.ID, has)
		if err != nil {
			return fmt.Errorf("set private has_scheduled: %w", err)
		}
		return nil
	case domain.PeerTypeChannel:
		_, err := db.Exec(ctx, `
UPDATE channel_dialogs
SET has_scheduled = $3,
    updated_at = now()
WHERE user_id = $1
  AND channel_id = $2`, ownerUserID, peer.ID, has)
		if err != nil {
			return fmt.Errorf("set channel has_scheduled: %w", err)
		}
		return nil
	default:
		return nil
	}
}

func refreshHasScheduledTx(ctx context.Context, db pgx.Tx, ownerUserID int64, peer domain.Peer) error {
	var exists bool
	if err := db.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM scheduled_messages
  WHERE owner_user_id = $1
    AND peer_type = $2
    AND peer_id = $3
    AND state IN ('pending', 'dispatching')
  LIMIT 1
)::boolean`, ownerUserID, string(peer.Type), peer.ID).Scan(&exists); err != nil {
		return fmt.Errorf("refresh scheduled flag: %w", err)
	}
	return setHasScheduledTx(ctx, db, ownerUserID, peer, exists)
}

func intSliceToInt32(ids []int) []int32 {
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		if id > 0 && id <= domain.MaxMessageBoxID {
			out = append(out, int32(id))
		}
	}
	return out
}

func scheduledMessageListHash(messages []domain.ScheduledMessage) int64 {
	h := fnv.New64a()
	var buf [8]byte
	for _, msg := range messages {
		binary.LittleEndian.PutUint64(buf[:], uint64(msg.ID))
		_, _ = h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], uint64(msg.Peer.ID))
		_, _ = h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], uint64(msg.ScheduleDate))
		_, _ = h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], uint64(msg.UpdatedAt))
		_, _ = h.Write(buf[:])
	}
	return int64(h.Sum64() & 0x7fffffffffffffff)
}
