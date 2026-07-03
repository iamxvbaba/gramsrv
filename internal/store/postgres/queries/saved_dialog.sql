-- Saved Messages 分会话（saved dialogs）查询组。
-- 子会话 = self-chat box 行按 saved_peer 分组；top message 不物化，由
-- MAX(box_id) 聚合现算（box_id 单调于时间，date 序与 id 序一致），
-- 删除/清史后的一致性由查询本身保证。

-- name: ListSavedDialogTops :many
WITH tops AS (
  SELECT m.saved_peer_type, m.saved_peer_id, MAX(m.box_id) AS top_box_id
  FROM message_boxes m
  WHERE m.owner_user_id = sqlc.arg(owner_user_id)::bigint
    AND m.peer_type = 'user'
    AND m.peer_id = sqlc.arg(owner_user_id)::bigint
    AND NOT m.deleted
    AND m.saved_peer_type <> ''
  GROUP BY m.saved_peer_type, m.saved_peer_id
)
SELECT
  t.saved_peer_type AS dialog_peer_type,
  t.saved_peer_id AS dialog_peer_id,
  (p.user_id IS NOT NULL)::boolean AS dialog_pinned,
  m.box_id,
  m.private_message_id,
  m.owner_user_id,
  m.peer_type,
  m.peer_id,
  m.from_user_id,
  m.message_date,
  m.ttl_period,
  m.expires_at,
  m.edit_date,
  m.hide_edited,
  m.outgoing,
  m.body,
  m.entities::text AS entities_json,
  m.silent,
  m.noforwards,
  m.reply_to_msg_id,
  m.reply_to_peer_type,
  m.reply_to_peer_id,
  m.reply_to_top_id,
  m.reply_to_story_id,
  m.quote_text,
  m.quote_entities::text AS quote_entities_json,
  m.quote_offset,
  m.fwd_from_peer_type,
  m.fwd_from_peer_id,
  m.fwd_from_name,
  m.fwd_date,
  m.fwd_saved_from_peer_type,
  m.fwd_saved_from_peer_id,
  m.fwd_saved_from_msg_id,
  m.saved_peer_type,
  m.saved_peer_id,
  m.pts,
  m.media::text AS media_json,
  m.media_unread,
  m.reaction_unread,
  m.via_bot_id,
  m.grouped_id,
  m.effect,
  m.reply_markup::text AS reply_markup_json,
  m.rich_message::text AS rich_message_json,
  m.pinned
FROM tops t
JOIN message_boxes m
  ON m.owner_user_id = sqlc.arg(owner_user_id)::bigint
 AND m.box_id = t.top_box_id
LEFT JOIN saved_dialog_pins p
  ON p.user_id = sqlc.arg(owner_user_id)::bigint
 AND p.peer_type = t.saved_peer_type
 AND p.peer_id = t.saved_peer_id
WHERE (sqlc.arg(offset_id)::int <= 0 OR t.top_box_id < sqlc.arg(offset_id)::int)
  AND (NOT sqlc.arg(exclude_pinned)::boolean OR p.user_id IS NULL)
ORDER BY t.top_box_id DESC
LIMIT sqlc.arg(limit_count)::int;

-- name: ListPinnedSavedDialogTops :many
WITH tops AS (
  SELECT m.saved_peer_type, m.saved_peer_id, MAX(m.box_id) AS top_box_id
  FROM message_boxes m
  WHERE m.owner_user_id = sqlc.arg(owner_user_id)::bigint
    AND m.peer_type = 'user'
    AND m.peer_id = sqlc.arg(owner_user_id)::bigint
    AND NOT m.deleted
    AND m.saved_peer_type <> ''
  GROUP BY m.saved_peer_type, m.saved_peer_id
)
SELECT
  t.saved_peer_type AS dialog_peer_type,
  t.saved_peer_id AS dialog_peer_id,
  true::boolean AS dialog_pinned,
  m.box_id,
  m.private_message_id,
  m.owner_user_id,
  m.peer_type,
  m.peer_id,
  m.from_user_id,
  m.message_date,
  m.ttl_period,
  m.expires_at,
  m.edit_date,
  m.hide_edited,
  m.outgoing,
  m.body,
  m.entities::text AS entities_json,
  m.silent,
  m.noforwards,
  m.reply_to_msg_id,
  m.reply_to_peer_type,
  m.reply_to_peer_id,
  m.reply_to_top_id,
  m.reply_to_story_id,
  m.quote_text,
  m.quote_entities::text AS quote_entities_json,
  m.quote_offset,
  m.fwd_from_peer_type,
  m.fwd_from_peer_id,
  m.fwd_from_name,
  m.fwd_date,
  m.fwd_saved_from_peer_type,
  m.fwd_saved_from_peer_id,
  m.fwd_saved_from_msg_id,
  m.saved_peer_type,
  m.saved_peer_id,
  m.pts,
  m.media::text AS media_json,
  m.media_unread,
  m.reaction_unread,
  m.via_bot_id,
  m.grouped_id,
  m.effect,
  m.reply_markup::text AS reply_markup_json,
  m.rich_message::text AS rich_message_json,
  m.pinned
FROM saved_dialog_pins p
JOIN tops t
  ON t.saved_peer_type = p.peer_type
 AND t.saved_peer_id = p.peer_id
JOIN message_boxes m
  ON m.owner_user_id = sqlc.arg(owner_user_id)::bigint
 AND m.box_id = t.top_box_id
WHERE p.user_id = sqlc.arg(owner_user_id)::bigint
ORDER BY p.pinned_order ASC, p.peer_id ASC;

-- name: ListSavedDialogTopsByPeers :many
WITH requested AS (
  SELECT
    (sqlc.arg(peer_types)::text[])[i] AS peer_type,
    (sqlc.arg(peer_ids)::bigint[])[i] AS peer_id
  FROM generate_subscripts(sqlc.arg(peer_ids)::bigint[], 1) AS g(i)
  WHERE i <= cardinality(sqlc.arg(peer_types)::text[])
),
tops AS (
  SELECT m.saved_peer_type, m.saved_peer_id, MAX(m.box_id) AS top_box_id
  FROM message_boxes m
  JOIN requested r
    ON r.peer_type = m.saved_peer_type
   AND r.peer_id = m.saved_peer_id
  WHERE m.owner_user_id = sqlc.arg(owner_user_id)::bigint
    AND m.peer_type = 'user'
    AND m.peer_id = sqlc.arg(owner_user_id)::bigint
    AND NOT m.deleted
  GROUP BY m.saved_peer_type, m.saved_peer_id
)
SELECT
  t.saved_peer_type AS dialog_peer_type,
  t.saved_peer_id AS dialog_peer_id,
  (p.user_id IS NOT NULL)::boolean AS dialog_pinned,
  m.box_id,
  m.private_message_id,
  m.owner_user_id,
  m.peer_type,
  m.peer_id,
  m.from_user_id,
  m.message_date,
  m.ttl_period,
  m.expires_at,
  m.edit_date,
  m.hide_edited,
  m.outgoing,
  m.body,
  m.entities::text AS entities_json,
  m.silent,
  m.noforwards,
  m.reply_to_msg_id,
  m.reply_to_peer_type,
  m.reply_to_peer_id,
  m.reply_to_top_id,
  m.reply_to_story_id,
  m.quote_text,
  m.quote_entities::text AS quote_entities_json,
  m.quote_offset,
  m.fwd_from_peer_type,
  m.fwd_from_peer_id,
  m.fwd_from_name,
  m.fwd_date,
  m.fwd_saved_from_peer_type,
  m.fwd_saved_from_peer_id,
  m.fwd_saved_from_msg_id,
  m.saved_peer_type,
  m.saved_peer_id,
  m.pts,
  m.media::text AS media_json,
  m.media_unread,
  m.reaction_unread,
  m.via_bot_id,
  m.grouped_id,
  m.effect,
  m.reply_markup::text AS reply_markup_json,
  m.rich_message::text AS rich_message_json,
  m.pinned
FROM tops t
JOIN message_boxes m
  ON m.owner_user_id = sqlc.arg(owner_user_id)::bigint
 AND m.box_id = t.top_box_id
LEFT JOIN saved_dialog_pins p
  ON p.user_id = sqlc.arg(owner_user_id)::bigint
 AND p.peer_type = t.saved_peer_type
 AND p.peer_id = t.saved_peer_id
ORDER BY t.top_box_id DESC;

-- name: CountSavedDialogs :one
SELECT count(*)::int AS dialog_count
FROM (
  SELECT m.saved_peer_type, m.saved_peer_id
  FROM message_boxes m
  WHERE m.owner_user_id = sqlc.arg(owner_user_id)::bigint
    AND m.peer_type = 'user'
    AND m.peer_id = sqlc.arg(owner_user_id)::bigint
    AND NOT m.deleted
    AND m.saved_peer_type <> ''
  GROUP BY m.saved_peer_type, m.saved_peer_id
) d
LEFT JOIN saved_dialog_pins p
  ON p.user_id = sqlc.arg(owner_user_id)::bigint
 AND p.peer_type = d.saved_peer_type
 AND p.peer_id = d.saved_peer_id
WHERE NOT sqlc.arg(exclude_pinned)::boolean OR p.user_id IS NULL;

-- name: CountSavedDialogPins :one
SELECT count(*)::int AS pin_count
FROM saved_dialog_pins
WHERE user_id = $1;

-- name: UpsertSavedDialogPinFront :execrows
INSERT INTO saved_dialog_pins (user_id, peer_type, peer_id, pinned_order)
VALUES (
  sqlc.arg(user_id)::bigint,
  sqlc.arg(peer_type)::text,
  sqlc.arg(peer_id)::bigint,
  COALESCE((SELECT MIN(pinned_order) - 1 FROM saved_dialog_pins WHERE user_id = sqlc.arg(user_id)::bigint), 0)
)
ON CONFLICT (user_id, peer_type, peer_id) DO NOTHING;

-- name: DeleteSavedDialogPin :execrows
DELETE FROM saved_dialog_pins
WHERE user_id = $1
  AND peer_type = $2
  AND peer_id = $3;

-- name: ClearSavedDialogPinsNotInOrder :exec
DELETE FROM saved_dialog_pins p
WHERE p.user_id = sqlc.arg(user_id)::bigint
  AND NOT EXISTS (
    SELECT 1
    FROM generate_subscripts(sqlc.arg(peer_ids)::bigint[], 1) AS g(i)
    WHERE i <= cardinality(sqlc.arg(peer_types)::text[])
      AND (sqlc.arg(peer_types)::text[])[i] = p.peer_type
      AND (sqlc.arg(peer_ids)::bigint[])[i] = p.peer_id
  );

-- name: ReorderSavedDialogPins :exec
INSERT INTO saved_dialog_pins (user_id, peer_type, peer_id, pinned_order)
SELECT
  sqlc.arg(user_id)::bigint,
  (sqlc.arg(peer_types)::text[])[i],
  (sqlc.arg(peer_ids)::bigint[])[i],
  (i - 1)::int
FROM generate_subscripts(sqlc.arg(peer_ids)::bigint[], 1) AS g(i)
WHERE i <= cardinality(sqlc.arg(peer_types)::text[])
ON CONFLICT (user_id, peer_type, peer_id) DO UPDATE SET pinned_order = EXCLUDED.pinned_order;

-- name: DeleteMessageBoxesBySavedPeerBatch :many
WITH target AS (
  SELECT
    m.owner_user_id,
    m.box_id
  FROM message_boxes m
  WHERE m.owner_user_id = sqlc.arg(owner_user_id)::bigint
    AND m.peer_type = 'user'
    AND m.peer_id = sqlc.arg(owner_user_id)::bigint
    AND m.saved_peer_type = sqlc.arg(saved_peer_type)::text
    AND m.saved_peer_id = sqlc.arg(saved_peer_id)::bigint
    AND (sqlc.arg(max_id)::int <= 0 OR m.box_id <= sqlc.arg(max_id)::int)
    AND (sqlc.arg(min_date)::int <= 0 OR m.message_date >= sqlc.arg(min_date)::int)
    AND (sqlc.arg(max_date)::int <= 0 OR m.message_date <= sqlc.arg(max_date)::int)
    AND NOT m.deleted
  ORDER BY m.box_id DESC
  LIMIT sqlc.arg(limit_count)::int
  FOR UPDATE SKIP LOCKED
),
updated AS (
  UPDATE message_boxes m
  SET deleted = true
  FROM target t
  WHERE m.owner_user_id = t.owner_user_id
    AND m.box_id = t.box_id
  RETURNING
    m.owner_user_id,
    m.box_id,
    m.private_message_id,
    m.message_sender_id,
    m.peer_type,
    m.peer_id
)
SELECT
  owner_user_id,
  box_id,
  private_message_id,
  message_sender_id,
  peer_type,
  peer_id
FROM updated
ORDER BY box_id ASC;

-- name: HasDeletableMessageBoxBySavedPeer :one
SELECT EXISTS (
  SELECT 1
  FROM message_boxes m
  WHERE m.owner_user_id = sqlc.arg(owner_user_id)::bigint
    AND m.peer_type = 'user'
    AND m.peer_id = sqlc.arg(owner_user_id)::bigint
    AND m.saved_peer_type = sqlc.arg(saved_peer_type)::text
    AND m.saved_peer_id = sqlc.arg(saved_peer_id)::bigint
    AND (sqlc.arg(max_id)::int <= 0 OR m.box_id <= sqlc.arg(max_id)::int)
    AND (sqlc.arg(min_date)::int <= 0 OR m.message_date >= sqlc.arg(min_date)::int)
    AND (sqlc.arg(max_date)::int <= 0 OR m.message_date <= sqlc.arg(max_date)::int)
    AND NOT m.deleted
  LIMIT 1
)::boolean AS more;
