package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// BotAPIUpdateStore persists Bot API getUpdates queues in PostgreSQL.
type BotAPIUpdateStore struct {
	db sqlcgen.DBTX
}

func NewBotAPIUpdateStore(db sqlcgen.DBTX) *BotAPIUpdateStore {
	return &BotAPIUpdateStore{db: db}
}

func (s *BotAPIUpdateStore) SetBotAPIWebhook(ctx context.Context, config domain.BotAPIWebhook, dropPending bool) error {
	if config.BotUserID <= 0 || config.URL == "" || config.MaxConnections < 1 || config.MaxConnections > 100 {
		return fmt.Errorf("invalid bot api webhook")
	}
	var allowed []string
	if len(config.AllowedUpdates) > 0 {
		allowed = make([]string, 0, len(config.AllowedUpdates))
		for _, kind := range config.AllowedUpdates {
			if kind != "" {
				allowed = append(allowed, string(kind))
			}
		}
	}
	if _, err := s.db.Exec(ctx, `
WITH policy AS (
  SELECT CASE WHEN $6::boolean THEN $5::text[]
         ELSE (SELECT allowed_updates FROM bot_api_update_states WHERE bot_user_id = $1)
         END AS allowed_updates
), configured AS (
  INSERT INTO bot_api_webhooks (
    bot_user_id, url, secret_token, max_connections, allowed_updates,
    failure_count, last_error_date, last_error_message, next_attempt_at,
    delivery_owner, delivery_expires_at, updated_at
  )
  SELECT $1, $2, $3, $4, allowed_updates, 0, 0, '', now(), '', NULL, now()
  FROM policy
  ON CONFLICT (bot_user_id) DO UPDATE
  SET url = EXCLUDED.url,
      secret_token = EXCLUDED.secret_token,
      max_connections = EXCLUDED.max_connections,
      allowed_updates = EXCLUDED.allowed_updates,
      failure_count = 0,
      last_error_date = 0,
      last_error_message = '',
      next_attempt_at = now(),
      delivery_owner = '',
      delivery_expires_at = NULL,
      updated_at = now()
  RETURNING bot_user_id
), boundary AS (
  SELECT CASE WHEN $7::boolean THEN COALESCE(MAX(id), 0) ELSE 0 END AS confirmed_update_id
  FROM bot_api_updates
  WHERE bot_user_id = $1
)
INSERT INTO bot_api_update_states (
  bot_user_id, confirmed_update_id, allowed_updates, cursor_initialized
)
SELECT $1, confirmed_update_id, policy.allowed_updates, $7::boolean
FROM boundary, configured, policy
ON CONFLICT (bot_user_id) DO UPDATE
SET confirmed_update_id = CASE WHEN $7::boolean
        THEN GREATEST(bot_api_update_states.confirmed_update_id, EXCLUDED.confirmed_update_id)
        ELSE bot_api_update_states.confirmed_update_id
    END,
    allowed_updates = EXCLUDED.allowed_updates,
    cursor_initialized = bot_api_update_states.cursor_initialized OR EXCLUDED.cursor_initialized,
    updated_at = now()
`, config.BotUserID, config.URL, config.SecretToken, config.MaxConnections, allowed,
		config.AllowedUpdatesSet, dropPending); err != nil {
		return fmt.Errorf("set bot api webhook: %w", err)
	}
	return nil
}

func (s *BotAPIUpdateStore) DeleteBotAPIWebhook(ctx context.Context, botUserID int64, dropPending bool) error {
	if botUserID <= 0 {
		return nil
	}
	if _, err := s.db.Exec(ctx, `
WITH deleted AS (
  DELETE FROM bot_api_webhooks WHERE bot_user_id = $1 RETURNING bot_user_id
), boundary AS (
  SELECT CASE WHEN $2::boolean THEN COALESCE(MAX(id), 0) ELSE 0 END AS confirmed_update_id
  FROM bot_api_updates
  WHERE bot_user_id = $1
)
INSERT INTO bot_api_update_states (bot_user_id, confirmed_update_id, cursor_initialized)
SELECT $1, confirmed_update_id, $2::boolean
FROM boundary
ON CONFLICT (bot_user_id) DO UPDATE
SET confirmed_update_id = CASE WHEN $2::boolean
        THEN GREATEST(bot_api_update_states.confirmed_update_id, EXCLUDED.confirmed_update_id)
        ELSE bot_api_update_states.confirmed_update_id
    END,
    cursor_initialized = bot_api_update_states.cursor_initialized OR EXCLUDED.cursor_initialized,
    updated_at = now()
`, botUserID, dropPending); err != nil {
		return fmt.Errorf("delete bot api webhook: %w", err)
	}
	return nil
}

func (s *BotAPIUpdateStore) BotAPIWebhook(ctx context.Context, botUserID int64) (domain.BotAPIWebhook, bool, error) {
	config, err := scanBotAPIWebhook(s.db.QueryRow(ctx, `
SELECT bot_user_id, url, secret_token, max_connections, allowed_updates,
       failure_count, last_error_date, last_error_message, next_attempt_at
FROM bot_api_webhooks
WHERE bot_user_id = $1
`, botUserID))
	if err == pgx.ErrNoRows {
		return domain.BotAPIWebhook{}, false, nil
	}
	if err != nil {
		return domain.BotAPIWebhook{}, false, fmt.Errorf("get bot api webhook: %w", err)
	}
	return config, true, nil
}

func (s *BotAPIUpdateStore) ListDueBotAPIWebhooks(ctx context.Context, limit int) ([]domain.BotAPIWebhook, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
SELECT bot_user_id, url, secret_token, max_connections, allowed_updates,
       failure_count, last_error_date, last_error_message, next_attempt_at
FROM bot_api_webhooks
WHERE next_attempt_at <= now()
  AND (delivery_owner = '' OR delivery_expires_at <= now())
ORDER BY next_attempt_at, bot_user_id
LIMIT $1
`, limit)
	if err != nil {
		return nil, fmt.Errorf("list due bot api webhooks: %w", err)
	}
	defer rows.Close()
	out := make([]domain.BotAPIWebhook, 0, limit)
	for rows.Next() {
		config, err := scanBotAPIWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, config)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list due bot api webhook rows: %w", err)
	}
	return out, nil
}

func (s *BotAPIUpdateStore) AcquireBotAPIWebhookLease(ctx context.Context, botUserID int64, owner string, ttl time.Duration) (bool, error) {
	if botUserID <= 0 || owner == "" || ttl <= 0 {
		return false, fmt.Errorf("invalid bot api webhook lease")
	}
	var acquiredOwner string
	err := s.db.QueryRow(ctx, `
UPDATE bot_api_webhooks
SET delivery_owner = $2,
    delivery_expires_at = now() + make_interval(secs => $3),
    updated_at = now()
WHERE bot_user_id = $1
  AND (delivery_owner = $2 OR delivery_owner = '' OR delivery_expires_at <= now())
RETURNING delivery_owner
`, botUserID, owner, int64((ttl+time.Second-1)/time.Second)).Scan(&acquiredOwner)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("acquire bot api webhook lease: %w", err)
	}
	return acquiredOwner == owner, nil
}

func (s *BotAPIUpdateStore) ReleaseBotAPIWebhookLease(ctx context.Context, botUserID int64, owner string) error {
	if botUserID <= 0 || owner == "" {
		return nil
	}
	if _, err := s.db.Exec(ctx, `
UPDATE bot_api_webhooks
SET delivery_owner = '', delivery_expires_at = NULL, updated_at = now()
WHERE bot_user_id = $1 AND delivery_owner = $2
`, botUserID, owner); err != nil {
		return fmt.Errorf("release bot api webhook lease: %w", err)
	}
	return nil
}

func (s *BotAPIUpdateStore) RecordBotAPIWebhookFailure(ctx context.Context, botUserID int64, owner string, nextAttempt time.Time, message string) error {
	if len(message) > 512 {
		message = message[:512]
	}
	if _, err := s.db.Exec(ctx, `
UPDATE bot_api_webhooks
SET failure_count = failure_count + 1,
    last_error_date = EXTRACT(EPOCH FROM now())::integer,
    last_error_message = $3,
    next_attempt_at = $4,
    delivery_owner = '', delivery_expires_at = NULL, updated_at = now()
WHERE bot_user_id = $1 AND delivery_owner = $2
`, botUserID, owner, message, nextAttempt); err != nil {
		return fmt.Errorf("record bot api webhook failure: %w", err)
	}
	return nil
}

func (s *BotAPIUpdateStore) RecordBotAPIWebhookSuccess(ctx context.Context, botUserID int64, owner string, nextAttempt time.Time) error {
	if _, err := s.db.Exec(ctx, `
UPDATE bot_api_webhooks
SET failure_count = 0, last_error_date = 0, last_error_message = '',
    next_attempt_at = $3, delivery_owner = '', delivery_expires_at = NULL, updated_at = now()
WHERE bot_user_id = $1 AND delivery_owner = $2
`, botUserID, owner, nextAttempt); err != nil {
		return fmt.Errorf("record bot api webhook success: %w", err)
	}
	return nil
}

func scanBotAPIWebhook(row botAPIUpdateScanner) (domain.BotAPIWebhook, error) {
	var config domain.BotAPIWebhook
	var allowed []string
	if err := row.Scan(&config.BotUserID, &config.URL, &config.SecretToken, &config.MaxConnections, &allowed,
		&config.FailureCount, &config.LastErrorDate, &config.LastErrorMessage, &config.NextAttemptAt); err != nil {
		return domain.BotAPIWebhook{}, err
	}
	if allowed != nil {
		config.AllowedUpdates = make([]domain.BotAPIUpdateKind, 0, len(allowed))
		for _, kind := range allowed {
			config.AllowedUpdates = append(config.AllowedUpdates, domain.BotAPIUpdateKind(kind))
		}
	}
	return config, nil
}

func (s *BotAPIUpdateStore) AcquireBotAPIPollLease(ctx context.Context, botUserID int64, owner string, ttl time.Duration) (bool, error) {
	if botUserID <= 0 || owner == "" || ttl <= 0 {
		return false, fmt.Errorf("invalid bot api poll lease")
	}
	var acquiredOwner string
	err := s.db.QueryRow(ctx, `
INSERT INTO bot_api_update_states (
  bot_user_id, confirmed_update_id, poll_owner, poll_expires_at
) VALUES ($1, 0, $2, now() + make_interval(secs => $3))
ON CONFLICT (bot_user_id) DO UPDATE
SET poll_owner = EXCLUDED.poll_owner,
    poll_expires_at = EXCLUDED.poll_expires_at,
    updated_at = now()
WHERE bot_api_update_states.poll_owner = EXCLUDED.poll_owner
   OR bot_api_update_states.poll_expires_at IS NULL
   OR bot_api_update_states.poll_expires_at <= now()
RETURNING poll_owner
`, botUserID, owner, int64((ttl+time.Second-1)/time.Second)).Scan(&acquiredOwner)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("acquire bot api poll lease: %w", err)
	}
	return acquiredOwner == owner, nil
}

func (s *BotAPIUpdateStore) ReleaseBotAPIPollLease(ctx context.Context, botUserID int64, owner string) error {
	if botUserID <= 0 || owner == "" {
		return nil
	}
	if _, err := s.db.Exec(ctx, `
UPDATE bot_api_update_states
SET poll_owner = '', poll_expires_at = NULL, updated_at = now()
WHERE bot_user_id = $1 AND poll_owner = $2
`, botUserID, owner); err != nil {
		return fmt.Errorf("release bot api poll lease: %w", err)
	}
	return nil
}

func (s *BotAPIUpdateStore) EnqueueBotAPIUpdate(ctx context.Context, req domain.EnqueueBotAPIUpdateRequest) (domain.BotAPIUpdate, bool, error) {
	if err := validateBotAPIUpdateRequest(req); err != nil {
		return domain.BotAPIUpdate{}, false, err
	}
	var callbackQueryID, callbackUserID, callbackChatInstance int64
	var callbackInlineDCID, callbackInlineMessageID int
	var callbackInlineOwnerID, callbackInlineAccessHash int64
	var callbackData []byte
	if req.Callback != nil {
		callbackQueryID = req.Callback.ID
		callbackUserID = req.Callback.UserID
		callbackChatInstance = req.Callback.ChatInstance
		callbackData = req.Callback.Data
		if req.Callback.InlineMessage != nil {
			callbackInlineDCID = req.Callback.InlineMessage.DCID
			callbackInlineOwnerID = req.Callback.InlineMessage.OwnerID
			callbackInlineMessageID = req.Callback.InlineMessage.ID
			callbackInlineAccessHash = req.Callback.InlineMessage.AccessHash
		}
	}
	row, err := s.scanBotAPIUpdate(s.db.QueryRow(ctx, `
WITH inserted AS (
 INSERT INTO bot_api_updates (
  bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date,
  callback_query_id, callback_user_id, callback_chat_instance, callback_data,
  callback_inline_dc_id, callback_inline_owner_id, callback_inline_message_id, callback_inline_access_hash
) SELECT $1, $2::varchar(32), $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
WHERE NOT EXISTS (
  SELECT 1
  FROM bot_api_update_states
  WHERE bot_user_id = $1
    AND allowed_updates IS NOT NULL
    AND NOT ($2::text = ANY(allowed_updates))
)
 ON CONFLICT DO NOTHING
 RETURNING id, bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date,
           callback_query_id, callback_user_id, callback_chat_instance, callback_data,
           callback_inline_dc_id, callback_inline_owner_id, callback_inline_message_id, callback_inline_access_hash
), wake_webhook AS (
 UPDATE bot_api_webhooks
 SET next_attempt_at = now(), updated_at = now()
 WHERE bot_user_id = $1 AND EXISTS (SELECT 1 FROM inserted)
 RETURNING bot_user_id
)
SELECT id, bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date,
       callback_query_id, callback_user_id, callback_chat_instance, callback_data,
       callback_inline_dc_id, callback_inline_owner_id, callback_inline_message_id, callback_inline_access_hash
FROM inserted
`, req.BotUserID, string(req.Kind), string(req.Peer.Type), req.Peer.ID, req.MessageID, req.SourcePts, req.Date,
		callbackQueryID, callbackUserID, callbackChatInstance, callbackData,
		callbackInlineDCID, callbackInlineOwnerID, callbackInlineMessageID, callbackInlineAccessHash))
	if err == nil {
		return row, true, nil
	}
	if err != pgx.ErrNoRows {
		return domain.BotAPIUpdate{}, false, fmt.Errorf("insert bot api update: %w", err)
	}
	row, err = s.scanBotAPIUpdate(s.db.QueryRow(ctx, `
SELECT id, bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date,
       callback_query_id, callback_user_id, callback_chat_instance, callback_data,
       callback_inline_dc_id, callback_inline_owner_id, callback_inline_message_id, callback_inline_access_hash
FROM bot_api_updates
WHERE bot_user_id = $1
  AND update_kind = $2
  AND (
    (update_kind = 'callback_query' AND callback_query_id = $7)
    OR
    (update_kind <> 'callback_query' AND peer_type = $3 AND peer_id = $4 AND message_id = $5 AND source_pts = $6)
  )
`, req.BotUserID, string(req.Kind), string(req.Peer.Type), req.Peer.ID, req.MessageID, req.SourcePts, callbackQueryID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return domain.BotAPIUpdate{}, false, nil
		}
		return domain.BotAPIUpdate{}, false, fmt.Errorf("select existing bot api update: %w", err)
	}
	return row, false, nil
}

func (s *BotAPIUpdateStore) ListTailBotAPIUpdates(ctx context.Context, botUserID int64, tail, limit int) ([]domain.BotAPIUpdate, error) {
	if botUserID == 0 || tail <= 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
SELECT id, bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date,
       callback_query_id, callback_user_id, callback_chat_instance, callback_data,
       callback_inline_dc_id, callback_inline_owner_id, callback_inline_message_id, callback_inline_access_hash
FROM (
  SELECT id, bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date,
         callback_query_id, callback_user_id, callback_chat_instance, callback_data,
         callback_inline_dc_id, callback_inline_owner_id, callback_inline_message_id, callback_inline_access_hash
  FROM bot_api_updates
  WHERE bot_user_id = $1
    AND id > COALESCE((SELECT confirmed_update_id FROM bot_api_update_states WHERE bot_user_id = $1), 0)
  ORDER BY id DESC
  LIMIT $2
) AS tail_updates
ORDER BY id
LIMIT $3
`, botUserID, tail, limit)
	if err != nil {
		return nil, fmt.Errorf("list bot api tail updates: %w", err)
	}
	defer rows.Close()
	out := make([]domain.BotAPIUpdate, 0, limit)
	for rows.Next() {
		item, err := scanBotAPIUpdateRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bot api tail update rows: %w", err)
	}
	return out, nil
}

func (s *BotAPIUpdateStore) ListBotAPIUpdates(ctx context.Context, botUserID, fromUpdateID int64, limit int) ([]domain.BotAPIUpdate, error) {
	if botUserID == 0 {
		return nil, nil
	}
	if fromUpdateID <= 0 {
		fromUpdateID = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
SELECT id, bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date,
       callback_query_id, callback_user_id, callback_chat_instance, callback_data,
       callback_inline_dc_id, callback_inline_owner_id, callback_inline_message_id, callback_inline_access_hash
FROM bot_api_updates
WHERE bot_user_id = $1 AND id >= $2
ORDER BY id
LIMIT $3
`, botUserID, fromUpdateID, limit)
	if err != nil {
		return nil, fmt.Errorf("list bot api updates: %w", err)
	}
	defer rows.Close()
	out := make([]domain.BotAPIUpdate, 0, limit)
	for rows.Next() {
		item, err := scanBotAPIUpdateRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bot api updates rows: %w", err)
	}
	return out, nil
}

func (s *BotAPIUpdateStore) ConfirmBotAPIUpdates(ctx context.Context, botUserID, confirmedUpdateID int64) error {
	if botUserID == 0 || confirmedUpdateID <= 0 {
		return nil
	}
	if _, err := s.db.Exec(ctx, `
WITH bounded AS (
  SELECT COALESCE(MAX(id), 0) AS max_update_id,
         LEAST($2::bigint, COALESCE(MAX(id), 0)) AS confirmed_update_id
  FROM bot_api_updates
  WHERE bot_user_id = $1
)
INSERT INTO bot_api_update_states (bot_user_id, confirmed_update_id, cursor_initialized)
SELECT $1, confirmed_update_id, true
FROM bounded
ON CONFLICT (bot_user_id) DO UPDATE
SET confirmed_update_id = GREATEST(
        bot_api_update_states.confirmed_update_id,
        CASE
          WHEN $2::bigint > (SELECT max_update_id FROM bounded)
               AND bot_api_update_states.cursor_initialized
            THEN bot_api_update_states.confirmed_update_id
          ELSE EXCLUDED.confirmed_update_id
        END
    ),
    cursor_initialized = true,
    updated_at = now()
`, botUserID, confirmedUpdateID); err != nil {
		return fmt.Errorf("confirm bot api updates: %w", err)
	}
	return nil
}

func (s *BotAPIUpdateStore) SetBotAPIAllowedUpdates(ctx context.Context, botUserID int64, allowed []domain.BotAPIUpdateKind) error {
	if botUserID == 0 {
		return nil
	}
	var values []string
	if len(allowed) > 0 {
		values = make([]string, 0, len(allowed))
		for _, kind := range allowed {
			if kind != "" {
				values = append(values, string(kind))
			}
		}
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO bot_api_update_states (bot_user_id, confirmed_update_id, allowed_updates)
VALUES ($1, 0, $2::text[])
ON CONFLICT (bot_user_id) DO UPDATE
SET allowed_updates = EXCLUDED.allowed_updates,
    updated_at = now()
`, botUserID, values); err != nil {
		return fmt.Errorf("set bot api allowed updates: %w", err)
	}
	return nil
}

func (s *BotAPIUpdateStore) DropPendingBotAPIUpdates(ctx context.Context, botUserID int64) error {
	if botUserID == 0 {
		return nil
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO bot_api_update_states (bot_user_id, confirmed_update_id, cursor_initialized)
SELECT $1, COALESCE(MAX(id), 0), true
FROM bot_api_updates
WHERE bot_user_id = $1
ON CONFLICT (bot_user_id) DO UPDATE
SET confirmed_update_id = GREATEST(bot_api_update_states.confirmed_update_id, EXCLUDED.confirmed_update_id),
    cursor_initialized = true,
    updated_at = now()
`, botUserID); err != nil {
		return fmt.Errorf("drop pending bot api updates: %w", err)
	}
	return nil
}

func (s *BotAPIUpdateStore) PendingBotAPIUpdateCount(ctx context.Context, botUserID int64) (int, error) {
	if botUserID == 0 {
		return 0, nil
	}
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT COUNT(*)
FROM bot_api_updates
WHERE bot_user_id = $1
  AND id > COALESCE((SELECT confirmed_update_id FROM bot_api_update_states WHERE bot_user_id = $1), 0)
`, botUserID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count pending bot api updates: %w", err)
	}
	return count, nil
}

// DeleteDeliveredOrExpired 回收 Bot API 投递队列的死行（性能审计 H1）：
//  1. 已确认（id <= bot_api_update_states.confirmed_update_id）且入队超过 confirmedGrace 的行——
//     官方 Bot API 语义下确认即弃，getUpdates 的 fromID 恒 > confirmed，删除不影响任何读路径；
//     宽限仅防御 offset 回拨调试场景。
//  2. 按队列 created_at 超过 maxAge 的行（无论确认与否）——对齐官方「updates 服务器最多保留 24 小时」
//     语义，同时封顶 MTProto-only bot（从不调 getUpdates、无 state 行）成员身份带来的无界增长。
//
// 与 user_update_events 的「永久保留」约束无关：那是 TDesktop 账号级 differenceTooLong 缺陷所迫，
// Bot API 队列没有该约束。返回两步合计删除行数。
func (s *BotAPIUpdateStore) DeleteDeliveredOrExpired(ctx context.Context, confirmedGrace, maxAge time.Duration, limit int) (int, error) {
	if limit <= 0 {
		limit = 10000
	}
	if limit > 100000 {
		limit = 100000
	}
	total := 0
	if confirmedGrace > 0 {
		// 从 states 小表出发，每 bot 走 bot_api_updates_bot_scan_idx(bot_user_id, id) 范围扫描。
		tag, err := s.db.Exec(ctx, `
DELETE FROM bot_api_updates
WHERE id IN (
    SELECT u.id
    FROM bot_api_update_states s
    JOIN bot_api_updates u ON u.bot_user_id = s.bot_user_id AND u.id <= s.confirmed_update_id
    WHERE u.created_at < now() - make_interval(secs => $1)
    LIMIT $2
)`, int64(confirmedGrace/time.Second), limit)
		if err != nil {
			return total, fmt.Errorf("delete confirmed bot api updates: %w", err)
		}
		total += int(tag.RowsAffected())
	}
	if maxAge > 0 {
		cutoff := time.Now().Add(-maxAge)
		// 走 bot_api_updates_created_retention_idx(created_at, id)。
		tag, err := s.db.Exec(ctx, `
DELETE FROM bot_api_updates
WHERE id IN (
    SELECT id
    FROM bot_api_updates
    WHERE created_at < $1
    ORDER BY created_at, id
    LIMIT $2
)`, cutoff, limit)
		if err != nil {
			return total, fmt.Errorf("delete expired bot api updates: %w", err)
		}
		total += int(tag.RowsAffected())
	}
	return total, nil
}

func (s *BotAPIUpdateStore) ConfirmedBotAPIUpdateID(ctx context.Context, botUserID int64) (int64, bool, error) {
	if botUserID == 0 {
		return 0, false, nil
	}
	var id int64
	if err := s.db.QueryRow(ctx, `
SELECT confirmed_update_id
FROM bot_api_update_states
WHERE bot_user_id = $1
`, botUserID).Scan(&id); err != nil {
		if err == pgx.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("get bot api update state: %w", err)
	}
	return id, true, nil
}

func (s *BotAPIUpdateStore) scanBotAPIUpdate(row pgx.Row) (domain.BotAPIUpdate, error) {
	return scanBotAPIUpdateRows(row)
}

type botAPIUpdateScanner interface {
	Scan(dest ...any) error
}

func scanBotAPIUpdateRows(row botAPIUpdateScanner) (domain.BotAPIUpdate, error) {
	var item domain.BotAPIUpdate
	var kind, peerType string
	var callbackQueryID, callbackUserID, callbackChatInstance int64
	var callbackInlineDCID, callbackInlineMessageID int
	var callbackInlineOwnerID, callbackInlineAccessHash int64
	var callbackData []byte
	if err := row.Scan(&item.ID, &item.BotUserID, &kind, &peerType, &item.Peer.ID, &item.MessageID, &item.SourcePts, &item.Date,
		&callbackQueryID, &callbackUserID, &callbackChatInstance, &callbackData,
		&callbackInlineDCID, &callbackInlineOwnerID, &callbackInlineMessageID, &callbackInlineAccessHash); err != nil {
		return domain.BotAPIUpdate{}, err
	}
	item.Kind = domain.BotAPIUpdateKind(kind)
	item.Peer.Type = domain.PeerType(peerType)
	if item.Kind == domain.BotAPIUpdateCallbackQuery {
		item.Callback = &domain.BotCallbackQuery{
			ID:           callbackQueryID,
			BotUserID:    item.BotUserID,
			UserID:       callbackUserID,
			Peer:         item.Peer,
			MessageID:    item.MessageID,
			ChatInstance: callbackChatInstance,
			Data:         append([]byte(nil), callbackData...),
		}
		if callbackInlineMessageID > 0 {
			item.Callback.InlineMessage = &domain.BotInlineMessageID{DCID: callbackInlineDCID, OwnerID: callbackInlineOwnerID, ID: callbackInlineMessageID, AccessHash: callbackInlineAccessHash}
		}
	}
	return item, nil
}

func validateBotAPIUpdateRequest(req domain.EnqueueBotAPIUpdateRequest) error {
	if req.BotUserID == 0 {
		return fmt.Errorf("invalid bot api update")
	}
	if req.Kind != domain.BotAPIUpdateMessage && req.Kind != domain.BotAPIUpdateEditedMessage && req.Kind != domain.BotAPIUpdateCallbackQuery {
		return fmt.Errorf("invalid bot api update kind %q", req.Kind)
	}
	switch req.Peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		if req.Peer.ID <= 0 || req.MessageID <= 0 {
			return fmt.Errorf("invalid bot api update peer")
		}
	case "":
		if req.Kind != domain.BotAPIUpdateCallbackQuery || req.Peer.ID != 0 || req.MessageID != 0 {
			return fmt.Errorf("invalid bot api update peer")
		}
	default:
		return fmt.Errorf("invalid bot api update peer type %q", req.Peer.Type)
	}
	if req.Kind == domain.BotAPIUpdateCallbackQuery {
		cb := req.Callback
		if cb == nil || cb.ID == 0 || cb.BotUserID != req.BotUserID || cb.UserID <= 0 ||
			cb.Peer != req.Peer || cb.MessageID != req.MessageID || cb.ChatInstance == 0 ||
			len(cb.Data) > domain.MaxCallbackDataLen || req.SourcePts != 0 {
			return fmt.Errorf("invalid bot api callback query")
		}
		inline := cb.InlineMessage
		if req.MessageID == 0 && (inline == nil || inline.DCID <= 0 || inline.OwnerID <= 0 || inline.ID <= 0 || inline.AccessHash == 0) {
			return fmt.Errorf("invalid bot api inline callback query")
		}
		if req.MessageID > 0 && inline != nil {
			return fmt.Errorf("ambiguous bot api callback query")
		}
	} else if req.Callback != nil {
		return fmt.Errorf("unexpected bot api callback query")
	}
	return nil
}
