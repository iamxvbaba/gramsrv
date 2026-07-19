-- name: AppendUserUpdateEvent :exec
INSERT INTO user_update_events (
  user_id,
  pts,
  pts_count,
  date,
  event_type,
  event_bool,
  event_phone,
  event_peers,
  peer_settings,
  message_ids,
  dialog_filter,
  filter_order,
  folder_peers,
  story_payload,
  reaction_payload,
  emoji_status_payload,
  message_box_id,
  peer_type,
  peer_id,
  filter_id,
  max_id,
  still_unread_count,
  channel_pts,
  tags_enabled,
  folder_id
) VALUES (
  $1,
  $2,
  $3,
  $4,
  $5,
  sqlc.arg(event_bool)::boolean,
  sqlc.arg(event_phone)::text,
  sqlc.arg(event_peers)::jsonb,
  sqlc.arg(peer_settings)::jsonb,
  sqlc.arg(message_ids)::jsonb,
  sqlc.arg(dialog_filter)::jsonb,
  sqlc.arg(filter_order)::jsonb,
  sqlc.arg(folder_peers)::jsonb,
  sqlc.arg(story_payload)::jsonb,
  sqlc.arg(reaction_payload)::jsonb,
  sqlc.arg(emoji_status_payload)::jsonb,
  sqlc.narg(message_box_id),
  sqlc.narg(peer_type)::text,
  sqlc.narg(peer_id)::bigint,
  sqlc.arg(filter_id)::int,
  sqlc.arg(max_id)::int,
  sqlc.arg(still_unread_count)::int,
  sqlc.arg(channel_pts)::int,
  sqlc.arg(tags_enabled)::boolean,
  sqlc.arg(folder_id)::int
);

-- name: ListUserUpdateEventsAfter :many
SELECT
  e.user_id,
  e.pts,
  e.pts_count,
  e.date,
  e.event_type,
  e.event_bool,
  e.event_phone,
  COALESCE(e.event_peers::text, '[]')::text AS event_peers_json,
  COALESCE(e.peer_settings::text, '{}')::text AS peer_settings_json,
  COALESCE(e.message_ids::text, '[]')::text AS message_ids_json,
  COALESCE(e.dialog_filter::text, '{}')::text AS dialog_filter_json,
  COALESCE(e.filter_order::text, '[]')::text AS filter_order_json,
  COALESCE(e.folder_peers::text, '[]')::text AS folder_peers_json,
  COALESCE(e.story_payload::text, '{}')::text AS story_payload_json,
  COALESCE(e.reaction_payload::text, '{}')::text AS reaction_payload_json,
  COALESCE(e.emoji_status_payload::text, '{}')::text AS emoji_status_payload_json,
  COALESCE(e.peer_type, '')::text AS event_peer_type,
  COALESCE(e.peer_id, 0)::bigint AS event_peer_id,
  e.filter_id,
  e.max_id,
  e.still_unread_count,
  e.channel_pts,
  e.tags_enabled,
  e.folder_id,
  COALESCE(m.box_id, 0)::int AS message_id,
  COALESCE(m.private_message_id, 0)::bigint AS private_message_id,
  COALESCE(m.owner_user_id, 0)::bigint AS owner_user_id,
  COALESCE(m.peer_type, '')::text AS peer_type,
  COALESCE(m.peer_id, 0)::bigint AS peer_id,
  COALESCE(m.from_user_id, 0)::bigint AS from_user_id,
  COALESCE(m.message_date, 0)::int AS message_date,
  COALESCE(m.ttl_period, 0)::int AS ttl_period,
  COALESCE(m.expires_at, 0)::int AS expires_at,
  COALESCE(m.edit_date, 0)::int AS edit_date,
  COALESCE(m.hide_edited, false)::boolean AS hide_edited,
  COALESCE(m.outgoing, false)::boolean AS outgoing,
  COALESCE(m.body, '')::text AS body,
  COALESCE(m.entities::text, '[]')::text AS message_entities_json,
  COALESCE(m.silent, false)::boolean AS silent,
  COALESCE(m.noforwards, false)::boolean AS noforwards,
  COALESCE(m.reply_to_msg_id, 0)::int AS reply_to_msg_id,
  COALESCE(m.reply_to_peer_type, '')::text AS reply_to_peer_type,
  COALESCE(m.reply_to_peer_id, 0)::bigint AS reply_to_peer_id,
  COALESCE(m.reply_to_top_id, 0)::int AS reply_to_top_id,
  COALESCE(m.reply_to_story_id, 0)::int AS reply_to_story_id,
  COALESCE(m.quote_text, '')::text AS quote_text,
  COALESCE(m.quote_entities::text, '[]')::text AS quote_entities_json,
  COALESCE(m.quote_offset, 0)::int AS quote_offset,
  COALESCE(m.fwd_from_peer_type, '')::text AS fwd_from_peer_type,
  COALESCE(m.fwd_from_peer_id, 0)::bigint AS fwd_from_peer_id,
  COALESCE(m.fwd_from_name, '')::text AS fwd_from_name,
  COALESCE(m.fwd_date, 0)::int AS fwd_date,
  COALESCE(m.fwd_saved_from_peer_type, '')::text AS fwd_saved_from_peer_type,
  COALESCE(m.fwd_saved_from_peer_id, 0)::bigint AS fwd_saved_from_peer_id,
  COALESCE(m.fwd_saved_from_msg_id, 0)::int AS fwd_saved_from_msg_id,
  COALESCE(m.saved_peer_type, '')::text AS saved_peer_type,
  COALESCE(m.saved_peer_id, 0)::bigint AS saved_peer_id,
  COALESCE(m.media::text, '{}')::text AS media_json,
  COALESCE(m.media_unread, false)::boolean AS media_unread,
  COALESCE(m.reaction_unread, false)::boolean AS reaction_unread,
  COALESCE(m.pinned, false)::boolean AS pinned,
  COALESCE(m.via_bot_id, 0)::bigint AS via_bot_id,
  COALESCE(m.grouped_id, 0)::bigint AS grouped_id,
  COALESCE(m.effect, 0)::bigint AS effect,
  COALESCE(m.reply_markup::text, '{}')::text AS reply_markup_json,
  COALESCE(m.rich_message::text, '{}')::text AS rich_message_json,
  COALESCE(peer_u.id, 0)::bigint AS peer_user_id,
  COALESCE(peer_u.access_hash, 0)::bigint AS peer_access_hash,
  COALESCE(peer_u.phone, '')::text AS peer_phone,
  COALESCE(peer_u.first_name, '')::text AS peer_first_name,
  COALESCE(peer_u.last_name, '')::text AS peer_last_name,
  COALESCE(peer_u.username, '')::text AS peer_username,
  COALESCE(peer_u.country_code, '')::text AS peer_country_code,
  COALESCE(peer_u.verified, false)::boolean AS peer_verified,
  COALESCE(peer_u.support, false)::boolean AS peer_support,
  COALESCE(peer_u.is_bot, false)::boolean AS peer_is_bot,
  COALESCE(peer_u.bot_info_version, 0)::int AS peer_bot_info_version,
  COALESCE(EXTRACT(EPOCH FROM peer_u.premium_expires_at), 0)::bigint AS peer_premium_until,
  COALESCE(peer_u.emoji_status_document_id, 0)::bigint AS peer_emoji_status_document_id,
  COALESCE(peer_u.emoji_status_until, 0)::bigint AS peer_emoji_status_until,
  COALESCE(from_u.id, 0)::bigint AS from_user_user_id,
  COALESCE(from_u.access_hash, 0)::bigint AS from_user_access_hash,
  COALESCE(from_u.phone, '')::text AS from_user_phone,
  COALESCE(from_u.first_name, '')::text AS from_user_first_name,
  COALESCE(from_u.last_name, '')::text AS from_user_last_name,
  COALESCE(from_u.username, '')::text AS from_user_username,
  COALESCE(from_u.country_code, '')::text AS from_user_country_code,
  COALESCE(from_u.verified, false)::boolean AS from_user_verified,
  COALESCE(from_u.support, false)::boolean AS from_user_support,
  COALESCE(from_u.is_bot, false)::boolean AS from_user_is_bot,
  COALESCE(from_u.bot_info_version, 0)::int AS from_user_bot_info_version,
  COALESCE(EXTRACT(EPOCH FROM from_u.premium_expires_at), 0)::bigint AS from_user_premium_until,
  COALESCE(from_u.emoji_status_document_id, 0)::bigint AS from_user_emoji_status_document_id,
  COALESCE(from_u.emoji_status_until, 0)::bigint AS from_user_emoji_status_until,
  COALESCE(fwd_u.id, 0)::bigint AS fwd_user_id,
  COALESCE(fwd_u.access_hash, 0)::bigint AS fwd_user_access_hash,
  COALESCE(fwd_u.phone, '')::text AS fwd_user_phone,
  COALESCE(fwd_u.first_name, '')::text AS fwd_user_first_name,
  COALESCE(fwd_u.last_name, '')::text AS fwd_user_last_name,
  COALESCE(fwd_u.username, '')::text AS fwd_user_username,
  COALESCE(fwd_u.country_code, '')::text AS fwd_user_country_code,
  COALESCE(fwd_u.verified, false)::boolean AS fwd_user_verified,
  COALESCE(fwd_u.support, false)::boolean AS fwd_user_support,
  COALESCE(fwd_u.is_bot, false)::boolean AS fwd_user_is_bot,
  COALESCE(fwd_u.bot_info_version, 0)::int AS fwd_user_bot_info_version,
  COALESCE(EXTRACT(EPOCH FROM fwd_u.premium_expires_at), 0)::bigint AS fwd_user_premium_until,
  COALESCE(fwd_u.emoji_status_document_id, 0)::bigint AS fwd_user_emoji_status_document_id,
  COALESCE(fwd_u.emoji_status_until, 0)::bigint AS fwd_user_emoji_status_until,
  COALESCE(reply_u.id, 0)::bigint AS reply_user_id,
  COALESCE(reply_u.access_hash, 0)::bigint AS reply_user_access_hash,
  COALESCE(reply_u.phone, '')::text AS reply_user_phone,
  COALESCE(reply_u.first_name, '')::text AS reply_user_first_name,
  COALESCE(reply_u.last_name, '')::text AS reply_user_last_name,
  COALESCE(reply_u.username, '')::text AS reply_user_username,
  COALESCE(reply_u.country_code, '')::text AS reply_user_country_code,
  COALESCE(reply_u.verified, false)::boolean AS reply_user_verified,
  COALESCE(reply_u.support, false)::boolean AS reply_user_support,
  COALESCE(reply_u.is_bot, false)::boolean AS reply_user_is_bot,
  COALESCE(reply_u.bot_info_version, 0)::int AS reply_user_bot_info_version,
  COALESCE(EXTRACT(EPOCH FROM reply_u.premium_expires_at), 0)::bigint AS reply_user_premium_until,
  COALESCE(reply_u.emoji_status_document_id, 0)::bigint AS reply_user_emoji_status_document_id,
  COALESCE(reply_u.emoji_status_until, 0)::bigint AS reply_user_emoji_status_until
FROM user_update_events e
LEFT JOIN message_boxes m ON m.owner_user_id = e.user_id AND m.box_id = e.message_box_id
LEFT JOIN users peer_u ON m.peer_type = 'user' AND peer_u.id = m.peer_id
LEFT JOIN users from_u ON from_u.id = m.from_user_id
LEFT JOIN users fwd_u ON m.fwd_from_peer_type = 'user' AND fwd_u.id = m.fwd_from_peer_id
LEFT JOIN users reply_u ON m.reply_to_peer_type = 'user' AND reply_u.id = m.reply_to_peer_id
WHERE e.user_id = $1
  AND e.pts > $2
ORDER BY e.pts ASC
LIMIT sqlc.arg(limit_count);

-- name: MaxUserPts :one
SELECT COALESCE(MAX(pts), 0)::int AS max_pts
FROM user_update_events
WHERE user_id = $1;

-- name: GetUserUpdateWatermark :one
SELECT contiguous_pts
FROM user_update_watermarks
WHERE user_id = $1;

-- name: EnqueueDispatch :exec
INSERT INTO dispatch_outbox (
  target_user_id,
  pts,
  event_type,
  exclude_auth_key_id,
  exclude_session_id
) VALUES (
  $1, $2, $3, $4, $5
)
ON CONFLICT DO NOTHING;

-- name: ClaimDispatchOutbox :many
-- durable head 表只保留每用户一行，并同步 head 的 readiness。claim 先锁
-- lane head 再更新对应 outbox 行，既不会扫描 backlog，也不会并发领取同一用户。
WITH picked_heads AS (
  SELECT h.target_user_id, h.head_id
  FROM dispatch_outbox_user_heads h
  WHERE (
        h.status = 'pending'
        AND h.next_attempt_at <= now()
      )
      OR (
        h.status = 'dispatching'
        AND h.updated_at < now() - make_interval(secs => sqlc.arg(lease_seconds)::int)
      )
  ORDER BY h.next_attempt_at ASC, h.target_user_id ASC, h.head_pts ASC, h.head_id ASC
  LIMIT sqlc.arg(limit_count)
  FOR UPDATE OF h SKIP LOCKED
)
UPDATE dispatch_outbox d
SET
  status = 'dispatching',
  attempts = d.attempts + 1,
  updated_at = now()
FROM picked_heads p
WHERE d.target_user_id = p.target_user_id
  AND d.id = p.head_id
RETURNING
  d.id,
  d.target_user_id,
  d.pts,
  d.event_type,
  d.exclude_auth_key_id,
  d.exclude_session_id,
  d.attempts;

-- name: ClaimDispatchOutboxShards :many
-- 固定 logical shard 由 target_user_id 决定；运行时 worker 只领取分配给自己的
-- shard 集合，因此同一用户永远只有一条串行 lane，而不同用户可并行。
WITH picked_heads AS (
  SELECT h.target_user_id, h.head_id
  FROM dispatch_outbox_user_heads h
  -- 256 与 store.DispatchOutboxLogicalShards、0069 generated column 是同一
  -- schema 常量；不得随 worker 数变化。
  WHERE h.logical_shard = ANY(sqlc.arg(shard_ids)::smallint[])
    AND (
      (
        h.status = 'pending'
        AND h.next_attempt_at <= now()
      )
      OR (
        h.status = 'dispatching'
        AND h.updated_at < now() - make_interval(secs => sqlc.arg(lease_seconds)::int)
      )
    )
  ORDER BY h.next_attempt_at ASC, h.target_user_id ASC, h.head_pts ASC, h.head_id ASC
  LIMIT sqlc.arg(limit_count)
  FOR UPDATE OF h SKIP LOCKED
)
UPDATE dispatch_outbox d
SET
  status = 'dispatching',
  attempts = d.attempts + 1,
  updated_at = now()
FROM picked_heads p
WHERE d.target_user_id = p.target_user_id
  AND d.id = p.head_id
RETURNING
  d.id,
  d.target_user_id,
  d.pts,
  d.event_type,
  d.exclude_auth_key_id,
  d.exclude_session_id,
  d.attempts;

-- name: MarkDispatchDelivered :execrows
-- 方案 A：投递成功即删除。outbox 是任务队列，delivered 行无保留价值
-- （消息在 message_boxes、离线补偿在 user_update_events），删除让表维持「未完成任务」小稳态。
-- claim 的锁序是 user_heads→outbox；completion 必须先显式锁同一 head 再删 outbox，
-- 否则租约过期 claim 与完成恰好竞争时会形成 outbox→head / head→outbox 环路。
WITH locked_head AS MATERIALIZED (
  SELECT h.target_user_id
  FROM dispatch_outbox_user_heads h
  WHERE h.target_user_id = sqlc.arg(target_user_id)::bigint
  FOR UPDATE
)
DELETE FROM dispatch_outbox d
USING locked_head h
WHERE d.target_user_id = h.target_user_id
  AND d.id = sqlc.arg(id)::bigint
  AND d.status = 'dispatching'
  AND d.attempts = sqlc.arg(expected_attempts)::int;

-- name: MarkDispatchFailed :execrows
WITH locked_head AS MATERIALIZED (
  SELECT h.target_user_id
  FROM dispatch_outbox_user_heads h
  WHERE h.target_user_id = sqlc.arg(target_user_id)::bigint
  FOR UPDATE
)
UPDATE dispatch_outbox d
SET
  status = CASE WHEN d.attempts >= 5 THEN 'failed' ELSE 'pending' END,
  next_attempt_at = CASE
    WHEN d.attempts >= 5 THEN d.next_attempt_at
    ELSE now() + make_interval(secs => LEAST(60, d.attempts * d.attempts))
  END,
  last_error = sqlc.arg(last_error)::text,
  updated_at = now()
FROM locked_head h
WHERE d.target_user_id = h.target_user_id
  AND d.id = sqlc.arg(id)::bigint
  AND d.status = 'dispatching'
  AND d.attempts = sqlc.arg(expected_attempts)::int;

-- name: BatchListDispatchEvents :many
-- 按 (user_id, pts) 精确批量取账号事件，供 outbox worker 一次性加载一批 claim 的事件详情，
-- 取代逐条 ListUserUpdateEventsAfter。列与 ListUserUpdateEventsAfter 完全一致以复用转换逻辑。
SELECT
  e.user_id,
  e.pts,
  e.pts_count,
  e.date,
  e.event_type,
  e.event_bool,
  e.event_phone,
  COALESCE(e.event_peers::text, '[]')::text AS event_peers_json,
  COALESCE(e.peer_settings::text, '{}')::text AS peer_settings_json,
  COALESCE(e.message_ids::text, '[]')::text AS message_ids_json,
  COALESCE(e.dialog_filter::text, '{}')::text AS dialog_filter_json,
  COALESCE(e.filter_order::text, '[]')::text AS filter_order_json,
  COALESCE(e.folder_peers::text, '[]')::text AS folder_peers_json,
  COALESCE(e.story_payload::text, '{}')::text AS story_payload_json,
  COALESCE(e.reaction_payload::text, '{}')::text AS reaction_payload_json,
  COALESCE(e.emoji_status_payload::text, '{}')::text AS emoji_status_payload_json,
  COALESCE(e.peer_type, '')::text AS event_peer_type,
  COALESCE(e.peer_id, 0)::bigint AS event_peer_id,
  e.filter_id,
  e.max_id,
  e.still_unread_count,
  e.channel_pts,
  e.tags_enabled,
  e.folder_id,
  COALESCE(m.box_id, 0)::int AS message_id,
  COALESCE(m.private_message_id, 0)::bigint AS private_message_id,
  COALESCE(m.owner_user_id, 0)::bigint AS owner_user_id,
  COALESCE(m.peer_type, '')::text AS peer_type,
  COALESCE(m.peer_id, 0)::bigint AS peer_id,
  COALESCE(m.from_user_id, 0)::bigint AS from_user_id,
  COALESCE(m.message_date, 0)::int AS message_date,
  COALESCE(m.ttl_period, 0)::int AS ttl_period,
  COALESCE(m.expires_at, 0)::int AS expires_at,
  COALESCE(m.edit_date, 0)::int AS edit_date,
  COALESCE(m.hide_edited, false)::boolean AS hide_edited,
  COALESCE(m.outgoing, false)::boolean AS outgoing,
  COALESCE(m.body, '')::text AS body,
  COALESCE(m.entities::text, '[]')::text AS message_entities_json,
  COALESCE(m.silent, false)::boolean AS silent,
  COALESCE(m.noforwards, false)::boolean AS noforwards,
  COALESCE(m.reply_to_msg_id, 0)::int AS reply_to_msg_id,
  COALESCE(m.reply_to_peer_type, '')::text AS reply_to_peer_type,
  COALESCE(m.reply_to_peer_id, 0)::bigint AS reply_to_peer_id,
  COALESCE(m.reply_to_top_id, 0)::int AS reply_to_top_id,
  COALESCE(m.reply_to_story_id, 0)::int AS reply_to_story_id,
  COALESCE(m.quote_text, '')::text AS quote_text,
  COALESCE(m.quote_entities::text, '[]')::text AS quote_entities_json,
  COALESCE(m.quote_offset, 0)::int AS quote_offset,
  COALESCE(m.fwd_from_peer_type, '')::text AS fwd_from_peer_type,
  COALESCE(m.fwd_from_peer_id, 0)::bigint AS fwd_from_peer_id,
  COALESCE(m.fwd_from_name, '')::text AS fwd_from_name,
  COALESCE(m.fwd_date, 0)::int AS fwd_date,
  COALESCE(m.fwd_saved_from_peer_type, '')::text AS fwd_saved_from_peer_type,
  COALESCE(m.fwd_saved_from_peer_id, 0)::bigint AS fwd_saved_from_peer_id,
  COALESCE(m.fwd_saved_from_msg_id, 0)::int AS fwd_saved_from_msg_id,
  COALESCE(m.saved_peer_type, '')::text AS saved_peer_type,
  COALESCE(m.saved_peer_id, 0)::bigint AS saved_peer_id,
  COALESCE(m.media::text, '{}')::text AS media_json,
  COALESCE(m.media_unread, false)::boolean AS media_unread,
  COALESCE(m.reaction_unread, false)::boolean AS reaction_unread,
  COALESCE(m.pinned, false)::boolean AS pinned,
  COALESCE(m.via_bot_id, 0)::bigint AS via_bot_id,
  COALESCE(m.grouped_id, 0)::bigint AS grouped_id,
  COALESCE(m.effect, 0)::bigint AS effect,
  COALESCE(m.reply_markup::text, '{}')::text AS reply_markup_json,
  COALESCE(m.rich_message::text, '{}')::text AS rich_message_json,
  COALESCE(peer_u.id, 0)::bigint AS peer_user_id,
  COALESCE(peer_u.access_hash, 0)::bigint AS peer_access_hash,
  COALESCE(peer_u.phone, '')::text AS peer_phone,
  COALESCE(peer_u.first_name, '')::text AS peer_first_name,
  COALESCE(peer_u.last_name, '')::text AS peer_last_name,
  COALESCE(peer_u.username, '')::text AS peer_username,
  COALESCE(peer_u.country_code, '')::text AS peer_country_code,
  COALESCE(peer_u.verified, false)::boolean AS peer_verified,
  COALESCE(peer_u.support, false)::boolean AS peer_support,
  COALESCE(peer_u.is_bot, false)::boolean AS peer_is_bot,
  COALESCE(peer_u.bot_info_version, 0)::int AS peer_bot_info_version,
  COALESCE(EXTRACT(EPOCH FROM peer_u.premium_expires_at), 0)::bigint AS peer_premium_until,
  COALESCE(peer_u.emoji_status_document_id, 0)::bigint AS peer_emoji_status_document_id,
  COALESCE(peer_u.emoji_status_until, 0)::bigint AS peer_emoji_status_until,
  COALESCE(from_u.id, 0)::bigint AS from_user_user_id,
  COALESCE(from_u.access_hash, 0)::bigint AS from_user_access_hash,
  COALESCE(from_u.phone, '')::text AS from_user_phone,
  COALESCE(from_u.first_name, '')::text AS from_user_first_name,
  COALESCE(from_u.last_name, '')::text AS from_user_last_name,
  COALESCE(from_u.username, '')::text AS from_user_username,
  COALESCE(from_u.country_code, '')::text AS from_user_country_code,
  COALESCE(from_u.verified, false)::boolean AS from_user_verified,
  COALESCE(from_u.support, false)::boolean AS from_user_support,
  COALESCE(from_u.is_bot, false)::boolean AS from_user_is_bot,
  COALESCE(from_u.bot_info_version, 0)::int AS from_user_bot_info_version,
  COALESCE(EXTRACT(EPOCH FROM from_u.premium_expires_at), 0)::bigint AS from_user_premium_until,
  COALESCE(from_u.emoji_status_document_id, 0)::bigint AS from_user_emoji_status_document_id,
  COALESCE(from_u.emoji_status_until, 0)::bigint AS from_user_emoji_status_until,
  COALESCE(fwd_u.id, 0)::bigint AS fwd_user_id,
  COALESCE(fwd_u.access_hash, 0)::bigint AS fwd_user_access_hash,
  COALESCE(fwd_u.phone, '')::text AS fwd_user_phone,
  COALESCE(fwd_u.first_name, '')::text AS fwd_user_first_name,
  COALESCE(fwd_u.last_name, '')::text AS fwd_user_last_name,
  COALESCE(fwd_u.username, '')::text AS fwd_user_username,
  COALESCE(fwd_u.country_code, '')::text AS fwd_user_country_code,
  COALESCE(fwd_u.verified, false)::boolean AS fwd_user_verified,
  COALESCE(fwd_u.support, false)::boolean AS fwd_user_support,
  COALESCE(fwd_u.is_bot, false)::boolean AS fwd_user_is_bot,
  COALESCE(fwd_u.bot_info_version, 0)::int AS fwd_user_bot_info_version,
  COALESCE(EXTRACT(EPOCH FROM fwd_u.premium_expires_at), 0)::bigint AS fwd_user_premium_until,
  COALESCE(fwd_u.emoji_status_document_id, 0)::bigint AS fwd_user_emoji_status_document_id,
  COALESCE(fwd_u.emoji_status_until, 0)::bigint AS fwd_user_emoji_status_until,
  COALESCE(reply_u.id, 0)::bigint AS reply_user_id,
  COALESCE(reply_u.access_hash, 0)::bigint AS reply_user_access_hash,
  COALESCE(reply_u.phone, '')::text AS reply_user_phone,
  COALESCE(reply_u.first_name, '')::text AS reply_user_first_name,
  COALESCE(reply_u.last_name, '')::text AS reply_user_last_name,
  COALESCE(reply_u.username, '')::text AS reply_user_username,
  COALESCE(reply_u.country_code, '')::text AS reply_user_country_code,
  COALESCE(reply_u.verified, false)::boolean AS reply_user_verified,
  COALESCE(reply_u.support, false)::boolean AS reply_user_support,
  COALESCE(reply_u.is_bot, false)::boolean AS reply_user_is_bot,
  COALESCE(reply_u.bot_info_version, 0)::int AS reply_user_bot_info_version,
  COALESCE(EXTRACT(EPOCH FROM reply_u.premium_expires_at), 0)::bigint AS reply_user_premium_until,
  COALESCE(reply_u.emoji_status_document_id, 0)::bigint AS reply_user_emoji_status_document_id,
  COALESCE(reply_u.emoji_status_until, 0)::bigint AS reply_user_emoji_status_until
FROM unnest(@user_ids::bigint[]) WITH ORDINALITY AS u(user_id, ord)
JOIN unnest(@pts_list::int[]) WITH ORDINALITY AS p(pts, ord) USING (ord)
JOIN user_update_events e ON e.user_id = u.user_id AND e.pts = p.pts
LEFT JOIN message_boxes m ON m.owner_user_id = e.user_id AND m.box_id = e.message_box_id
LEFT JOIN users peer_u ON m.peer_type = 'user' AND peer_u.id = m.peer_id
LEFT JOIN users from_u ON from_u.id = m.from_user_id
LEFT JOIN users fwd_u ON m.fwd_from_peer_type = 'user' AND fwd_u.id = m.fwd_from_peer_id
LEFT JOIN users reply_u ON m.reply_to_peer_type = 'user' AND reply_u.id = m.reply_to_peer_id;

-- name: MarkDispatchDeliveredBatch :execrows
-- 批量删除一批已投递的 (target_user_id, id)；target_user_id 入 WHERE 命中唯一索引并避免串删。
WITH input AS MATERIALIZED (
  SELECT tu.target_user_id, di.id, ea.attempts
  FROM unnest(@target_user_ids::bigint[]) WITH ORDINALITY AS tu(target_user_id, ord)
  JOIN unnest(@ids::bigint[]) WITH ORDINALITY AS di(id, ord) USING (ord)
  JOIN unnest(@expected_attempts::int[]) WITH ORDINALITY AS ea(attempts, ord) USING (ord)
),
locked_heads AS MATERIALIZED (
  SELECT h.target_user_id
  FROM dispatch_outbox_user_heads h
  JOIN (SELECT DISTINCT target_user_id FROM input) i USING (target_user_id)
  -- Match ClaimDispatchOutbox[Shards] exactly. A stale-lease claim may lock several
  -- dispatching heads while this completion batch locks the same set; a different
  -- multi-row order would merely move the deadlock one level up.
  ORDER BY h.next_attempt_at, h.target_user_id, h.head_pts, h.head_id
  FOR UPDATE OF h
)
DELETE FROM dispatch_outbox d
USING input i, locked_heads h
WHERE d.target_user_id = h.target_user_id
  AND d.target_user_id = i.target_user_id
  AND d.id = i.id
  AND d.status = 'dispatching'
  AND d.attempts = i.attempts;

-- name: DeleteFailedDispatchOutbox :one
-- failed 只能成为 lane head；从 head 表开始并先锁 head，既走 0074 的小索引，也与
-- claim/completion 保持同一 user_heads→outbox 锁序。删除的只是在线任务，durable
-- user_update_events 不动，故客户端仍可经 difference 恢复。
WITH doomed AS MATERIALIZED (
  SELECT h.target_user_id, h.head_id AS id
  FROM dispatch_outbox_user_heads h
  WHERE h.status = 'failed'
    AND h.updated_at < now() - make_interval(secs => sqlc.arg(older_than_seconds)::int)
  ORDER BY h.updated_at ASC, h.target_user_id ASC, h.head_id ASC
  LIMIT sqlc.arg(limit_count)
  FOR UPDATE OF h SKIP LOCKED
),
deleted AS (
  DELETE FROM dispatch_outbox d
  USING doomed x
  WHERE d.target_user_id = x.target_user_id
    AND d.id = x.id
  RETURNING d.id
)
SELECT count(*)::int AS deleted_count
FROM deleted;
