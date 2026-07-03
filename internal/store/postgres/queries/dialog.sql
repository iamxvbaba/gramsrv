-- name: ListDialogsByUser :many
WITH base AS (
  SELECT
    d.user_id,
    d.peer_type,
    d.peer_id,
    d.folder_id,
    d.top_message_id,
    d.top_message_date,
    d.read_inbox_max_id,
    d.read_outbox_max_id,
    d.unread_count,
    d.unread_mentions_count,
    d.unread_reactions_count,
    d.ttl_period,
    d.theme_emoticon,
    d.has_scheduled,
    d.pinned,
    d.pinned_order,
    d.unread_mark,
    d.hidden_peer_settings_bar,
    COALESCE(u.id, 0)::bigint AS peer_user_id,
    COALESCE(u.access_hash, 0)::bigint AS peer_access_hash,
    COALESCE(NULLIF(c.contact_phone, ''), u.phone, '')::text AS peer_phone,
    COALESCE(NULLIF(c.contact_first_name, ''), u.first_name, '')::text AS peer_first_name,
    COALESCE(c.contact_last_name, u.last_name, '')::text AS peer_last_name,
    COALESCE(u.username, '')::text AS peer_username,
    COALESCE(u.country_code, '')::text AS peer_country_code,
    COALESCE(u.verified, false)::boolean AS peer_verified,
    COALESCE(u.support, false)::boolean AS peer_support,
    COALESCE(u.is_bot, false)::boolean AS peer_is_bot,
    COALESCE(u.bot_info_version, 0)::int AS peer_bot_info_version,
    COALESCE(EXTRACT(EPOCH FROM u.premium_expires_at), 0)::bigint AS peer_premium_until,
    COALESCE(u.emoji_status_document_id, 0)::bigint AS peer_emoji_status_document_id,
    COALESCE(u.emoji_status_until, 0)::bigint AS peer_emoji_status_until,
    COALESCE(u.last_seen_at, 0)::bigint AS peer_last_seen_at,
    (c.contact_user_id IS NOT NULL)::boolean AS peer_contact,
    COALESCE(c.mutual, false)::boolean AS peer_mutual,
    COALESCE(m.box_id, 0)::int AS message_id,
    COALESCE(m.private_message_id, 0)::bigint AS message_private_message_id,
    COALESCE(m.from_user_id, 0)::bigint AS message_from_user_id,
    COALESCE(m.message_date, 0)::int AS message_date,
    COALESCE(m.outgoing, false)::boolean AS message_outgoing,
    COALESCE(m.body, '')::text AS message_body,
    COALESCE(m.entities::text, '[]')::text AS message_entities_json,
    COALESCE(m.media::text, '{}')::text AS message_media_json,
    COALESCE(m.ttl_period, 0)::int AS message_ttl_period,
    COALESCE(m.expires_at, 0)::int AS message_expires_at,
    COALESCE(m.edit_date, 0)::int AS message_edit_date,
    COALESCE(m.hide_edited, false)::boolean AS message_hide_edited,
    COALESCE(m.silent, false)::boolean AS message_silent,
    COALESCE(m.noforwards, false)::boolean AS message_noforwards,
    COALESCE(m.reply_to_msg_id, 0)::int AS message_reply_to_msg_id,
    COALESCE(m.reply_to_peer_type, '')::text AS message_reply_to_peer_type,
    COALESCE(m.reply_to_peer_id, 0)::bigint AS message_reply_to_peer_id,
    COALESCE(m.reply_to_top_id, 0)::int AS message_reply_to_top_id,
    COALESCE(m.reply_to_story_id, 0)::int AS message_reply_to_story_id,
    COALESCE(m.quote_text, '')::text AS message_quote_text,
    COALESCE(m.quote_entities::text, '[]')::text AS message_quote_entities_json,
    COALESCE(m.quote_offset, 0)::int AS message_quote_offset,
    COALESCE(m.fwd_from_peer_type, '')::text AS message_fwd_from_peer_type,
    COALESCE(m.fwd_from_peer_id, 0)::bigint AS message_fwd_from_peer_id,
    COALESCE(m.fwd_from_name, '')::text AS message_fwd_from_name,
    COALESCE(m.fwd_date, 0)::int AS message_fwd_date,
    COALESCE(m.fwd_saved_from_peer_type, '')::text AS message_fwd_saved_from_peer_type,
    COALESCE(m.fwd_saved_from_peer_id, 0)::bigint AS message_fwd_saved_from_peer_id,
    COALESCE(m.fwd_saved_from_msg_id, 0)::int AS message_fwd_saved_from_msg_id,
    COALESCE(m.saved_peer_type, '')::text AS message_saved_peer_type,
    COALESCE(m.saved_peer_id, 0)::bigint AS message_saved_peer_id,
    COALESCE(m.media_unread, false)::boolean AS message_media_unread,
    COALESCE(m.reaction_unread, false)::boolean AS message_reaction_unread,
    COALESCE(m.via_bot_id, 0)::bigint AS message_via_bot_id,
    COALESCE(m.grouped_id, 0)::bigint AS message_grouped_id,
    COALESCE(m.effect, 0)::bigint AS message_effect,
    COALESCE(m.reply_markup::text, '{}')::text AS message_reply_markup_json,
    COALESCE(m.rich_message::text, '{}')::text AS message_rich_message_json,
    COALESCE(m.pinned, false)::boolean AS message_pinned
  FROM dialogs d
  LEFT JOIN users u ON d.peer_type = 'user' AND u.id = d.peer_id
  LEFT JOIN contacts c ON d.peer_type = 'user' AND c.user_id = d.user_id AND c.contact_user_id = d.peer_id
  LEFT JOIN message_boxes m ON m.owner_user_id = d.user_id AND m.box_id = d.top_message_id AND NOT m.deleted
  WHERE d.user_id = $1
    AND (
      -- 不带 folder_id flag 视为主列表（folder 0）：官方语义下归档对话只以
      -- dialogFolder 聚合条目出现在主列表，DrKLO Android 主列表请求不设 flag。
      (NOT sqlc.arg(has_folder_id)::boolean AND d.folder_id = 0)
      OR (
        sqlc.arg(has_folder_id)::boolean
        AND sqlc.arg(folder_id)::int < 2
        AND d.folder_id = sqlc.arg(folder_id)::int
      )
      OR (
        sqlc.arg(folder_id)::int >= 2
        AND NOT (sqlc.arg(folder_exclude_archived)::boolean AND d.folder_id = 1)
        AND NOT (sqlc.arg(folder_exclude_read)::boolean AND d.unread_count = 0 AND NOT d.unread_mark)
        AND NOT EXISTS (
          SELECT 1
          FROM (
            SELECT fpt.peer_type, fpi.peer_id
            FROM unnest(sqlc.arg(folder_exclude_peer_types)::text[]) WITH ORDINALITY AS fpt(peer_type, ord)
            JOIN unnest(sqlc.arg(folder_exclude_peer_ids)::bigint[]) WITH ORDINALITY AS fpi(peer_id, ord) USING (ord)
          ) fp
          WHERE fp.peer_type = d.peer_type AND fp.peer_id = d.peer_id
        )
        AND (
          EXISTS (
            SELECT 1
            FROM (
              SELECT fpt.peer_type, fpi.peer_id
              FROM unnest(sqlc.arg(folder_include_peer_types)::text[]) WITH ORDINALITY AS fpt(peer_type, ord)
              JOIN unnest(sqlc.arg(folder_include_peer_ids)::bigint[]) WITH ORDINALITY AS fpi(peer_id, ord) USING (ord)
            ) fp
            WHERE fp.peer_type = d.peer_type AND fp.peer_id = d.peer_id
          )
          OR EXISTS (
            SELECT 1
            FROM (
              SELECT fpt.peer_type, fpi.peer_id
              FROM unnest(sqlc.arg(folder_pinned_peer_types)::text[]) WITH ORDINALITY AS fpt(peer_type, ord)
              JOIN unnest(sqlc.arg(folder_pinned_peer_ids)::bigint[]) WITH ORDINALITY AS fpi(peer_id, ord) USING (ord)
            ) fp
            WHERE fp.peer_type = d.peer_type AND fp.peer_id = d.peer_id
          )
          OR (sqlc.arg(folder_contacts)::boolean AND c.contact_user_id IS NOT NULL)
          OR (sqlc.arg(folder_non_contacts)::boolean AND c.contact_user_id IS NULL)
        )
      )
    )
    AND (NOT sqlc.arg(pinned_only)::boolean OR d.pinned)
    AND (NOT sqlc.arg(exclude_pinned)::boolean OR NOT d.pinned)
),
paged AS (
  SELECT *
  FROM base
  WHERE (
    (sqlc.arg(offset_date)::int <= 0 AND sqlc.arg(offset_id)::int <= 0)
    OR (
      sqlc.arg(offset_date)::int > 0
      AND (
        top_message_date < sqlc.arg(offset_date)::int
        OR (
          top_message_date = sqlc.arg(offset_date)::int
          AND (
            sqlc.arg(offset_id)::int <= 0
            OR top_message_id < sqlc.arg(offset_id)::int
            OR (
              top_message_id = sqlc.arg(offset_id)::int
              AND sqlc.arg(has_offset_peer)::boolean
              AND peer_id < sqlc.arg(offset_peer_id)::bigint
            )
          )
        )
      )
    )
    OR (
      sqlc.arg(offset_date)::int <= 0
      AND sqlc.arg(offset_id)::int > 0
      AND (
        top_message_id < sqlc.arg(offset_id)::int
        OR (
          top_message_id = sqlc.arg(offset_id)::int
          AND sqlc.arg(has_offset_peer)::boolean
          AND peer_id < sqlc.arg(offset_peer_id)::bigint
        )
      )
    )
  )
)
SELECT
  user_id,
  peer_type::text AS peer_type,
  peer_id::bigint AS peer_id,
  folder_id,
  top_message_id,
  top_message_date,
  read_inbox_max_id,
  read_outbox_max_id,
  unread_count,
  unread_mentions_count,
  unread_reactions_count,
  ttl_period,
  theme_emoticon,
  has_scheduled,
  pinned,
  pinned_order,
  unread_mark,
  hidden_peer_settings_bar,
  peer_user_id,
  peer_access_hash,
  peer_phone,
  peer_first_name,
  peer_last_name,
  peer_username,
  peer_country_code,
  peer_verified,
  peer_support,
  peer_is_bot,
  peer_bot_info_version,
  peer_premium_until,
  peer_emoji_status_document_id,
  peer_emoji_status_until,
  peer_last_seen_at,
  peer_contact,
  peer_mutual,
  message_id,
  message_private_message_id,
  message_from_user_id,
  message_date,
  message_outgoing,
  message_body,
  message_entities_json,
  message_media_json,
  message_ttl_period,
  message_expires_at,
  message_edit_date,
  message_hide_edited,
  message_silent,
  message_noforwards,
  message_reply_to_msg_id,
  message_reply_to_peer_type,
  message_reply_to_peer_id,
  message_reply_to_top_id,
  message_reply_to_story_id,
  message_quote_text,
  message_quote_entities_json,
  message_quote_offset,
  message_fwd_from_peer_type,
  message_fwd_from_peer_id,
  message_fwd_from_name,
  message_fwd_date,
  message_fwd_saved_from_peer_type,
  message_fwd_saved_from_peer_id,
  message_fwd_saved_from_msg_id,
  message_saved_peer_type,
  message_saved_peer_id,
  message_media_unread,
  message_reaction_unread,
  message_via_bot_id,
  message_grouped_id,
  message_effect,
  message_reply_markup_json,
  message_rich_message_json,
  message_pinned
FROM paged
ORDER BY
  pinned DESC,
  CASE WHEN pinned THEN COALESCE(pinned_order, 0) ELSE 0 END DESC,
  top_message_date DESC,
  top_message_id DESC,
  peer_id DESC
LIMIT sqlc.arg(limit_count);

-- name: ListDialogSummaryByUser :many
SELECT
  d.peer_type,
  d.peer_id,
  d.folder_id,
  d.top_message_id,
  d.top_message_date,
  d.read_inbox_max_id,
  d.read_outbox_max_id,
  d.unread_count,
  d.unread_mentions_count,
  d.unread_reactions_count,
  d.ttl_period,
  d.theme_emoticon,
  d.has_scheduled,
  d.pinned,
  d.pinned_order,
  d.unread_mark,
  d.hidden_peer_settings_bar
FROM dialogs d
LEFT JOIN contacts c ON d.peer_type = 'user' AND c.user_id = d.user_id AND c.contact_user_id = d.peer_id
WHERE d.user_id = $1
  AND (
    (NOT sqlc.arg(has_folder_id)::boolean AND d.folder_id = 0)
    OR (
      sqlc.arg(has_folder_id)::boolean
      AND sqlc.arg(folder_id)::int < 2
      AND d.folder_id = sqlc.arg(folder_id)::int
    )
    OR (
      sqlc.arg(folder_id)::int >= 2
      AND NOT (sqlc.arg(folder_exclude_archived)::boolean AND d.folder_id = 1)
      AND NOT (sqlc.arg(folder_exclude_read)::boolean AND d.unread_count = 0 AND NOT d.unread_mark)
      AND NOT EXISTS (
        SELECT 1
        FROM (
          SELECT fpt.peer_type, fpi.peer_id
          FROM unnest(sqlc.arg(folder_exclude_peer_types)::text[]) WITH ORDINALITY AS fpt(peer_type, ord)
          JOIN unnest(sqlc.arg(folder_exclude_peer_ids)::bigint[]) WITH ORDINALITY AS fpi(peer_id, ord) USING (ord)
        ) fp
        WHERE fp.peer_type = d.peer_type AND fp.peer_id = d.peer_id
      )
      AND (
        EXISTS (
          SELECT 1
          FROM (
            SELECT fpt.peer_type, fpi.peer_id
            FROM unnest(sqlc.arg(folder_include_peer_types)::text[]) WITH ORDINALITY AS fpt(peer_type, ord)
            JOIN unnest(sqlc.arg(folder_include_peer_ids)::bigint[]) WITH ORDINALITY AS fpi(peer_id, ord) USING (ord)
          ) fp
          WHERE fp.peer_type = d.peer_type AND fp.peer_id = d.peer_id
        )
        OR EXISTS (
          SELECT 1
          FROM (
            SELECT fpt.peer_type, fpi.peer_id
            FROM unnest(sqlc.arg(folder_pinned_peer_types)::text[]) WITH ORDINALITY AS fpt(peer_type, ord)
            JOIN unnest(sqlc.arg(folder_pinned_peer_ids)::bigint[]) WITH ORDINALITY AS fpi(peer_id, ord) USING (ord)
          ) fp
          WHERE fp.peer_type = d.peer_type AND fp.peer_id = d.peer_id
        )
        OR (sqlc.arg(folder_contacts)::boolean AND c.contact_user_id IS NOT NULL)
        OR (sqlc.arg(folder_non_contacts)::boolean AND c.contact_user_id IS NULL)
      )
    )
  )
  AND (NOT sqlc.arg(pinned_only)::boolean OR d.pinned)
  AND (NOT sqlc.arg(exclude_pinned)::boolean OR NOT d.pinned)
ORDER BY
  d.pinned DESC,
  CASE WHEN d.pinned THEN COALESCE(d.pinned_order, 0) ELSE 0 END DESC,
  d.top_message_date DESC,
  d.top_message_id DESC,
  d.peer_id DESC;

-- name: ListDialogsByPeers :many
WITH requested AS (
  SELECT
    (sqlc.arg(peer_types)::text[])[i] AS peer_type,
    (sqlc.arg(peer_ids)::bigint[])[i] AS peer_id,
    i::int AS ord
  FROM generate_subscripts(sqlc.arg(peer_ids)::bigint[], 1) AS g(i)
  WHERE i <= cardinality(sqlc.arg(peer_types)::text[])
),
deduped AS (
  SELECT DISTINCT ON (peer_type, peer_id)
    peer_type,
    peer_id,
    ord
  FROM requested
  ORDER BY peer_type, peer_id, ord
),
base AS (
  SELECT
    sqlc.arg(user_id)::bigint AS user_id,
    r.peer_type,
    r.peer_id,
    COALESCE(d.folder_id, 0)::int AS folder_id,
    COALESCE(d.top_message_id, 0)::int AS top_message_id,
    COALESCE(d.top_message_date, 0)::int AS top_message_date,
    COALESCE(d.read_inbox_max_id, 0)::int AS read_inbox_max_id,
    COALESCE(d.read_outbox_max_id, 0)::int AS read_outbox_max_id,
    COALESCE(d.unread_count, 0)::int AS unread_count,
    COALESCE(d.unread_mentions_count, 0)::int AS unread_mentions_count,
    COALESCE(d.unread_reactions_count, 0)::int AS unread_reactions_count,
    COALESCE(d.ttl_period, 0)::int AS ttl_period,
    COALESCE(d.theme_emoticon, '')::text AS theme_emoticon,
    COALESCE(d.has_scheduled, false)::boolean AS has_scheduled,
    COALESCE(d.pinned, false)::boolean AS pinned,
    COALESCE(d.pinned_order, 0)::int AS pinned_order,
    COALESCE(d.unread_mark, false)::boolean AS unread_mark,
    COALESCE(d.hidden_peer_settings_bar, false)::boolean AS hidden_peer_settings_bar,
    COALESCE(u.id, 0)::bigint AS peer_user_id,
    COALESCE(u.access_hash, 0)::bigint AS peer_access_hash,
    COALESCE(NULLIF(c.contact_phone, ''), u.phone, '')::text AS peer_phone,
    COALESCE(NULLIF(c.contact_first_name, ''), u.first_name, '')::text AS peer_first_name,
    COALESCE(c.contact_last_name, u.last_name, '')::text AS peer_last_name,
    COALESCE(u.username, '')::text AS peer_username,
    COALESCE(u.country_code, '')::text AS peer_country_code,
    COALESCE(u.verified, false)::boolean AS peer_verified,
    COALESCE(u.support, false)::boolean AS peer_support,
    COALESCE(u.is_bot, false)::boolean AS peer_is_bot,
    COALESCE(u.bot_info_version, 0)::int AS peer_bot_info_version,
    COALESCE(EXTRACT(EPOCH FROM u.premium_expires_at), 0)::bigint AS peer_premium_until,
    COALESCE(u.emoji_status_document_id, 0)::bigint AS peer_emoji_status_document_id,
    COALESCE(u.emoji_status_until, 0)::bigint AS peer_emoji_status_until,
    COALESCE(u.last_seen_at, 0)::bigint AS peer_last_seen_at,
    (c.contact_user_id IS NOT NULL)::boolean AS peer_contact,
    COALESCE(c.mutual, false)::boolean AS peer_mutual,
    COALESCE(m.box_id, 0)::int AS message_id,
    COALESCE(m.private_message_id, 0)::bigint AS message_private_message_id,
    COALESCE(m.from_user_id, 0)::bigint AS message_from_user_id,
    COALESCE(m.message_date, 0)::int AS message_date,
    COALESCE(m.outgoing, false)::boolean AS message_outgoing,
    COALESCE(m.body, '')::text AS message_body,
    COALESCE(m.entities::text, '[]')::text AS message_entities_json,
    COALESCE(m.media::text, '{}')::text AS message_media_json,
    COALESCE(m.ttl_period, 0)::int AS message_ttl_period,
    COALESCE(m.expires_at, 0)::int AS message_expires_at,
    COALESCE(m.edit_date, 0)::int AS message_edit_date,
    COALESCE(m.hide_edited, false)::boolean AS message_hide_edited,
    COALESCE(m.silent, false)::boolean AS message_silent,
    COALESCE(m.noforwards, false)::boolean AS message_noforwards,
    COALESCE(m.reply_to_msg_id, 0)::int AS message_reply_to_msg_id,
    COALESCE(m.reply_to_peer_type, '')::text AS message_reply_to_peer_type,
    COALESCE(m.reply_to_peer_id, 0)::bigint AS message_reply_to_peer_id,
    COALESCE(m.reply_to_top_id, 0)::int AS message_reply_to_top_id,
    COALESCE(m.reply_to_story_id, 0)::int AS message_reply_to_story_id,
    COALESCE(m.quote_text, '')::text AS message_quote_text,
    COALESCE(m.quote_entities::text, '[]')::text AS message_quote_entities_json,
    COALESCE(m.quote_offset, 0)::int AS message_quote_offset,
    COALESCE(m.fwd_from_peer_type, '')::text AS message_fwd_from_peer_type,
    COALESCE(m.fwd_from_peer_id, 0)::bigint AS message_fwd_from_peer_id,
    COALESCE(m.fwd_from_name, '')::text AS message_fwd_from_name,
    COALESCE(m.fwd_date, 0)::int AS message_fwd_date,
    COALESCE(m.fwd_saved_from_peer_type, '')::text AS message_fwd_saved_from_peer_type,
    COALESCE(m.fwd_saved_from_peer_id, 0)::bigint AS message_fwd_saved_from_peer_id,
    COALESCE(m.fwd_saved_from_msg_id, 0)::int AS message_fwd_saved_from_msg_id,
    COALESCE(m.saved_peer_type, '')::text AS message_saved_peer_type,
    COALESCE(m.saved_peer_id, 0)::bigint AS message_saved_peer_id,
    COALESCE(m.media_unread, false)::boolean AS message_media_unread,
    COALESCE(m.reaction_unread, false)::boolean AS message_reaction_unread,
    COALESCE(m.via_bot_id, 0)::bigint AS message_via_bot_id,
    COALESCE(m.grouped_id, 0)::bigint AS message_grouped_id,
    COALESCE(m.effect, 0)::bigint AS message_effect,
    COALESCE(m.reply_markup::text, '{}')::text AS message_reply_markup_json,
    COALESCE(m.rich_message::text, '{}')::text AS message_rich_message_json,
    COALESCE(m.pinned, false)::boolean AS message_pinned,
    r.ord
  FROM deduped r
  LEFT JOIN dialogs d
    ON d.user_id = sqlc.arg(user_id)::bigint
    AND d.peer_type = r.peer_type
    AND d.peer_id = r.peer_id
  LEFT JOIN users u ON r.peer_type = 'user' AND u.id = r.peer_id
  LEFT JOIN contacts c ON r.peer_type = 'user' AND c.user_id = sqlc.arg(user_id)::bigint AND c.contact_user_id = r.peer_id
  LEFT JOIN message_boxes m ON m.owner_user_id = sqlc.arg(user_id)::bigint AND m.box_id = d.top_message_id AND NOT m.deleted
)
SELECT
  user_id,
  peer_type::text AS peer_type,
  peer_id::bigint AS peer_id,
  folder_id,
  top_message_id,
  top_message_date,
  read_inbox_max_id,
  read_outbox_max_id,
  unread_count,
  unread_mentions_count,
  unread_reactions_count,
  ttl_period,
  theme_emoticon,
  has_scheduled,
  pinned,
  pinned_order,
  unread_mark,
  hidden_peer_settings_bar,
  peer_user_id,
  peer_access_hash,
  peer_phone,
  peer_first_name,
  peer_last_name,
  peer_username,
  peer_country_code,
  peer_verified,
  peer_support,
  peer_is_bot,
  peer_bot_info_version,
  peer_premium_until,
  peer_emoji_status_document_id,
  peer_emoji_status_until,
  peer_last_seen_at,
  peer_contact,
  peer_mutual,
  message_id,
  message_private_message_id,
  message_from_user_id,
  message_date,
  message_outgoing,
  message_body,
  message_entities_json,
  message_media_json,
  message_ttl_period,
  message_expires_at,
  message_edit_date,
  message_hide_edited,
  message_silent,
  message_noforwards,
  message_reply_to_msg_id,
  message_reply_to_peer_type,
  message_reply_to_peer_id,
  message_reply_to_top_id,
  message_reply_to_story_id,
  message_quote_text,
  message_quote_entities_json,
  message_quote_offset,
  message_fwd_from_peer_type,
  message_fwd_from_peer_id,
  message_fwd_from_name,
  message_fwd_date,
  message_fwd_saved_from_peer_type,
  message_fwd_saved_from_peer_id,
  message_fwd_saved_from_msg_id,
  message_saved_peer_type,
  message_saved_peer_id,
  message_media_unread,
  message_reaction_unread,
  message_via_bot_id,
  message_grouped_id,
  message_effect,
  message_reply_markup_json,
  message_rich_message_json,
  message_pinned
FROM base
ORDER BY ord;

-- name: UpsertDialog :exec
INSERT INTO dialogs (
  user_id,
  peer_type,
  peer_id,
  top_message_id,
  top_message_date,
  read_inbox_max_id,
  read_outbox_max_id,
  unread_count,
  unread_mentions_count,
  unread_reactions_count,
  pinned,
  unread_mark
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
ON CONFLICT (user_id, peer_type, peer_id) DO UPDATE SET
  top_message_id = EXCLUDED.top_message_id,
  top_message_date = EXCLUDED.top_message_date,
  read_inbox_max_id = EXCLUDED.read_inbox_max_id,
  read_outbox_max_id = EXCLUDED.read_outbox_max_id,
  unread_count = EXCLUDED.unread_count,
  unread_mentions_count = EXCLUDED.unread_mentions_count,
  unread_reactions_count = EXCLUDED.unread_reactions_count,
  pinned = EXCLUDED.pinned,
  unread_mark = EXCLUDED.unread_mark,
  updated_at = now();

-- name: UpsertOutboxDialog :exec
-- 发送方向该会话发出消息即视为已知晓内容：清除手动未读标记，
-- 与 channel 发送路径、readHistory 清除语义对齐。
INSERT INTO dialogs (
  user_id,
  peer_type,
  peer_id,
  top_message_id,
  top_message_date,
  unread_count
) VALUES (
  $1, $2, $3, $4, $5, 0
)
ON CONFLICT (user_id, peer_type, peer_id) DO UPDATE SET
  top_message_id = EXCLUDED.top_message_id,
  top_message_date = EXCLUDED.top_message_date,
  unread_mark = false,
  updated_at = now();

-- name: UpsertInboxDialog :exec
INSERT INTO dialogs (
  user_id,
  peer_type,
  peer_id,
  top_message_id,
  top_message_date,
  unread_count
) VALUES (
  $1, $2, $3, $4, $5, 1
)
ON CONFLICT (user_id, peer_type, peer_id) DO UPDATE SET
  top_message_id = GREATEST(dialogs.top_message_id, EXCLUDED.top_message_id),
  top_message_date = CASE
    WHEN EXCLUDED.top_message_id >= dialogs.top_message_id THEN EXCLUDED.top_message_date
    ELSE dialogs.top_message_date
  END,
  unread_count = (
    SELECT COUNT(*)::int
    FROM message_boxes m
    WHERE m.owner_user_id = dialogs.user_id
      AND m.peer_type = dialogs.peer_type
      AND m.peer_id = dialogs.peer_id
      AND NOT m.deleted
      AND NOT m.outgoing
      AND m.box_id > dialogs.read_inbox_max_id
      AND m.box_id <= GREATEST(dialogs.top_message_id, EXCLUDED.top_message_id)
  ),
  updated_at = now();

-- name: MarkDialogRead :one
WITH target AS (
  SELECT
    d.user_id,
    d.peer_type,
    d.peer_id,
    d.top_message_id,
    d.read_inbox_max_id,
    d.unread_count,
    LEAST(
      d.top_message_id,
      CASE WHEN sqlc.arg(max_id)::int > 0 THEN sqlc.arg(max_id)::int ELSE d.top_message_id END
    )::int AS requested_read_max_id
  FROM dialogs d
  WHERE d.user_id = $1
    AND d.peer_type = $2
    AND d.peer_id = $3
),
updated AS (
UPDATE dialogs d
SET
  read_inbox_max_id = GREATEST(d.read_inbox_max_id, target.requested_read_max_id),
  unread_count = (
    SELECT count(*)::int
    FROM message_boxes m
    WHERE m.owner_user_id = d.user_id
      AND m.peer_type = d.peer_type
      AND m.peer_id = d.peer_id
      AND NOT m.deleted
      AND NOT m.outgoing
      AND m.box_id > GREATEST(d.read_inbox_max_id, target.requested_read_max_id)
  ),
  unread_mark = false,
  unread_mentions_count = 0,
  updated_at = now()
FROM target
WHERE d.user_id = target.user_id
  AND d.peer_type = target.peer_type
  AND d.peer_id = target.peer_id
RETURNING
  d.user_id,
  d.peer_type,
  d.peer_id,
  d.read_inbox_max_id,
  d.unread_count,
  (
    target.unread_count > 0
    OR target.requested_read_max_id > target.read_inbox_max_id
  )::boolean AS changed
)
SELECT
  user_id,
  peer_type,
  peer_id,
  read_inbox_max_id,
  unread_count,
  changed
FROM updated;

-- name: SetDialogPinned :one
-- 置顶顺序在 dialog 当前 folder（0 主列表/1 归档）内独立分配，
-- 返回 folder_id 供 updateDialogPinned.folder_id 使用。
WITH target AS (
  SELECT d.folder_id
  FROM dialogs d
  WHERE d.user_id = sqlc.arg(user_id)::bigint
    AND d.peer_type = sqlc.arg(peer_type)::text
    AND d.peer_id = sqlc.arg(peer_id)::bigint
),
next_order AS (
  -- order 空间跨 dialogs/channel_dialogs 两表统一（合并层按 pinned_order
  -- 全局排序），但仅在目标会话所在 folder 内取最大值。
  SELECT GREATEST(
    COALESCE((
      SELECT MAX(d.pinned_order)
      FROM dialogs d, target t
      WHERE d.user_id = sqlc.arg(user_id)::bigint
        AND d.pinned
        AND d.folder_id = t.folder_id
    ), 0),
    COALESCE((
      SELECT MAX(cd.pinned_order)
      FROM channel_dialogs cd, target t
      WHERE cd.user_id = sqlc.arg(user_id)::bigint
        AND cd.pinned
        AND COALESCE(cd.folder_id, 0) = t.folder_id
    ), 0)
  )::int + 1 AS value
),
updated AS (
  UPDATE dialogs d
  SET pinned = sqlc.arg(pinned)::boolean,
      pinned_order = CASE
        WHEN sqlc.arg(pinned)::boolean THEN
          CASE WHEN d.pinned_order > 0 THEN d.pinned_order ELSE next_order.value END
        ELSE 0
      END,
      updated_at = now()
  FROM next_order
  WHERE d.user_id = sqlc.arg(user_id)::bigint
    AND d.peer_type = sqlc.arg(peer_type)::text
    AND d.peer_id = sqlc.arg(peer_id)::bigint
  RETURNING d.folder_id
)
SELECT
  EXISTS (SELECT 1 FROM updated)::boolean AS changed,
  COALESCE((SELECT folder_id FROM updated), 0)::int AS folder_id;

-- name: SetDialogUnreadMark :one
-- 值守卫 IS DISTINCT FROM：重复标记同一值不应算 changed，否则上层会记一条
-- 幽灵 durable 未读标事件并多推一次 update（对齐频道侧 SetChannelDialogUnreadMark）。
WITH updated AS (
  UPDATE dialogs d
  SET unread_mark = sqlc.arg(unread)::boolean,
      updated_at = now()
  WHERE d.user_id = sqlc.arg(user_id)::bigint
    AND d.peer_type = sqlc.arg(peer_type)::text
    AND d.peer_id = sqlc.arg(peer_id)::bigint
    AND d.unread_mark IS DISTINCT FROM sqlc.arg(unread)::boolean
  RETURNING d.user_id
)
SELECT EXISTS (SELECT 1 FROM updated)::boolean AS changed;

-- name: ListDialogUnreadMarks :many
SELECT
  peer_type,
  peer_id
FROM dialogs
WHERE user_id = $1
  AND unread_mark
ORDER BY top_message_date DESC, top_message_id DESC, peer_id DESC;

-- name: SetPeerSettingsBarHidden :one
WITH updated AS (
  UPDATE dialogs d
  SET hidden_peer_settings_bar = true,
      updated_at = now()
  WHERE d.user_id = sqlc.arg(user_id)::bigint
    AND d.peer_type = sqlc.arg(peer_type)::text
    AND d.peer_id = sqlc.arg(peer_id)::bigint
  RETURNING d.user_id
)
SELECT EXISTS (SELECT 1 FROM updated)::boolean AS changed;

-- name: GetPeerSettingsBarHidden :one
SELECT hidden_peer_settings_bar
FROM dialogs
WHERE user_id = $1
  AND peer_type = $2
  AND peer_id = $3;

-- name: ReorderPinnedDialogs :exec
WITH requested AS (
  SELECT
    (sqlc.arg(peer_types)::text[])[i] AS peer_type,
    (sqlc.arg(peer_ids)::bigint[])[i] AS peer_id,
    i::int AS pos
  FROM generate_subscripts(sqlc.arg(peer_ids)::bigint[], 1) AS g(i)
  WHERE i <= cardinality(sqlc.arg(peer_types)::text[])
),
deduped AS (
  -- 请求数组首位是置顶区最顶部；统一编码为"值越大越靠前"，与频道
  -- reorder、toggle 的 MAX+1 分配以及合并层排序方向一致。
  SELECT DISTINCT ON (peer_type, peer_id)
    peer_type,
    peer_id,
    (cardinality(sqlc.arg(peer_ids)::bigint[]) - pos + 1)::int AS ord
  FROM requested
  ORDER BY peer_type, peer_id, pos
)
UPDATE dialogs d
SET pinned = true,
    pinned_order = deduped.ord,
    updated_at = now()
FROM deduped
WHERE d.user_id = sqlc.arg(user_id)::bigint
  AND d.peer_type = deduped.peer_type
  AND d.peer_id = deduped.peer_id
  AND d.folder_id = sqlc.arg(folder_id)::int;

-- name: EditDialogPeerFolders :exec
WITH requested AS (
  SELECT
    (sqlc.arg(peer_types)::text[])[i] AS peer_type,
    (sqlc.arg(peer_ids)::bigint[])[i] AS peer_id,
    (sqlc.arg(folder_ids)::int[])[i] AS folder_id
  FROM generate_subscripts(sqlc.arg(peer_ids)::bigint[], 1) AS g(i)
  WHERE i <= cardinality(sqlc.arg(peer_types)::text[])
    AND i <= cardinality(sqlc.arg(folder_ids)::int[])
),
deduped AS (
  SELECT DISTINCT ON (peer_type, peer_id)
    peer_type,
    peer_id,
    folder_id
  FROM requested
  WHERE folder_id IN (0, 1)
  ORDER BY peer_type, peer_id
)
UPDATE dialogs d
-- 换 folder 时清 pinned：TDesktop History::setFolderPointer 在归档/还原时
-- 本地无条件 unpin，服务端保留旧 pin 会在下次 getDialogs 时把状态漂移回来。
SET folder_id = deduped.folder_id,
    pinned = CASE WHEN d.folder_id <> deduped.folder_id THEN false ELSE d.pinned END,
    pinned_order = CASE WHEN d.folder_id <> deduped.folder_id THEN 0 ELSE d.pinned_order END,
    updated_at = now()
FROM deduped
WHERE d.user_id = sqlc.arg(user_id)::bigint
  AND d.peer_type = deduped.peer_type
  AND d.peer_id = deduped.peer_id;

-- name: SetDialogArchivePinned :one
-- archive folder 行本身的置顶状态（toggleDialogPin(inputDialogPeerFolder)）。
-- 无行时官方默认视为 pinned=true，所以这里总是落一行显式值。
WITH current AS (
  SELECT archive_pinned
  FROM dialog_filter_settings
  WHERE user_id = sqlc.arg(user_id)::bigint
),
upserted AS (
  INSERT INTO dialog_filter_settings (user_id, archive_pinned)
  VALUES (sqlc.arg(user_id)::bigint, sqlc.arg(pinned)::boolean)
  ON CONFLICT (user_id) DO UPDATE SET
    archive_pinned = EXCLUDED.archive_pinned,
    updated_at = now()
  RETURNING archive_pinned
)
SELECT (COALESCE((SELECT archive_pinned FROM current), true) IS DISTINCT FROM sqlc.arg(pinned)::boolean)::boolean AS changed
FROM upserted;

-- name: GetDialogArchivePinned :one
SELECT archive_pinned
FROM dialog_filter_settings
WHERE user_id = $1;

-- name: CountArchiveUnreadDialogs :one
-- 归档（folder_id=1）未读聚合：有未读或手动标记未读的会话数 + 未读消息总数。
SELECT
  COUNT(*) FILTER (WHERE unread_count > 0 OR unread_mark)::int AS unread_peers,
  COALESCE(SUM(unread_count), 0)::int AS unread_messages
FROM dialogs
WHERE user_id = $1
  AND folder_id = 1;

-- name: ClearPinnedDialogsNotInOrder :exec
WITH requested AS (
  SELECT
    (sqlc.arg(peer_types)::text[])[i] AS peer_type,
    (sqlc.arg(peer_ids)::bigint[])[i] AS peer_id
  FROM generate_subscripts(sqlc.arg(peer_ids)::bigint[], 1) AS g(i)
  WHERE i <= cardinality(sqlc.arg(peer_types)::text[])
)
UPDATE dialogs d
SET pinned = false,
    pinned_order = 0,
    updated_at = now()
WHERE d.user_id = sqlc.arg(user_id)::bigint
  AND d.pinned
  AND d.folder_id = sqlc.arg(folder_id)::int
  AND NOT EXISTS (
    SELECT 1
    FROM requested r
    WHERE r.peer_type = d.peer_type
      AND r.peer_id = d.peer_id
  );

-- name: RefreshDialogAfterMessageDelete :exec
UPDATE dialogs d
SET
  top_message_id = sqlc.arg(top_message_id)::int,
  top_message_date = sqlc.arg(top_message_date)::int,
  unread_count = (
    SELECT COUNT(*)::int
    FROM message_boxes m
    WHERE m.owner_user_id = d.user_id
      AND m.peer_type = d.peer_type
      AND m.peer_id = d.peer_id
      AND NOT m.deleted
      AND NOT m.outgoing
      AND m.box_id > d.read_inbox_max_id
  ),
  unread_mentions_count = 0,
  unread_reactions_count = (
    SELECT COUNT(*)::int
    FROM message_boxes m2
    WHERE m2.owner_user_id = d.user_id
      AND m2.peer_type = d.peer_type
      AND m2.peer_id = d.peer_id
      AND NOT m2.deleted
      AND m2.reaction_unread
  ),
  updated_at = now()
WHERE d.user_id = sqlc.arg(user_id)::bigint
  AND d.peer_type = sqlc.arg(peer_type)::text
  AND d.peer_id = sqlc.arg(peer_id)::bigint;

-- name: ClearDialogAfterHistoryDelete :exec
UPDATE dialogs d
SET
  top_message_id = 0,
  top_message_date = 0,
  read_inbox_max_id = GREATEST(d.read_inbox_max_id, d.top_message_id),
  read_outbox_max_id = GREATEST(d.read_outbox_max_id, d.top_message_id),
  unread_count = 0,
  unread_mark = false,
  unread_mentions_count = 0,
  unread_reactions_count = (
    SELECT COUNT(*)::int
    FROM message_boxes m2
    WHERE m2.owner_user_id = d.user_id
      AND m2.peer_type = d.peer_type
      AND m2.peer_id = d.peer_id
      AND NOT m2.deleted
      AND m2.reaction_unread
  ),
  updated_at = now()
WHERE d.user_id = sqlc.arg(user_id)::bigint
  AND d.peer_type = sqlc.arg(peer_type)::text
  AND d.peer_id = sqlc.arg(peer_id)::bigint;

-- name: DeleteDialogByPeer :exec
WITH dropped_drafts AS (
    -- 删除会话同时丢弃该 peer 的云草稿，避免对端重建会话后旧草稿复活。
    DELETE FROM dialog_drafts dd
    WHERE dd.user_id = sqlc.arg(user_id)::bigint
      AND dd.peer_type = sqlc.arg(peer_type)::text
      AND dd.peer_id = sqlc.arg(peer_id)::bigint
)
DELETE FROM dialogs d
WHERE d.user_id = sqlc.arg(user_id)::bigint
  AND d.peer_type = sqlc.arg(peer_type)::text
  AND d.peer_id = sqlc.arg(peer_id)::bigint;

-- name: ListDialogFolders :many
SELECT
  filter_id,
  is_chatlist,
  filter::text AS filter_json
FROM dialog_filters
WHERE user_id = $1
ORDER BY order_value ASC, filter_id ASC;

-- name: GetDialogFolder :one
SELECT
  filter_id,
  is_chatlist,
  filter::text AS filter_json
FROM dialog_filters
WHERE user_id = $1
  AND filter_id = $2;

-- name: UpsertDialogFolder :exec
INSERT INTO dialog_filters (
  user_id,
  filter_id,
  is_chatlist,
  filter,
  order_value
) VALUES (
  $1,
  $2,
  $3,
  sqlc.arg(filter_json)::jsonb,
  COALESCE(
    (SELECT order_value FROM dialog_filters WHERE user_id = $1 AND filter_id = $2),
    (SELECT COALESCE(MAX(order_value), 0) + 1 FROM dialog_filters WHERE user_id = $1)
  )
)
ON CONFLICT (user_id, filter_id) DO UPDATE SET
  is_chatlist = EXCLUDED.is_chatlist,
  filter = EXCLUDED.filter,
  updated_at = now();

-- name: DeleteDialogFolder :exec
DELETE FROM dialog_filters
WHERE user_id = $1
  AND filter_id = $2;

-- name: ReorderDialogFolders :exec
WITH requested AS (
  SELECT filter_id, ord::int AS order_value
  FROM unnest(sqlc.arg(filter_ids)::int[]) WITH ORDINALITY AS t(filter_id, ord)
),
deduped AS (
  SELECT DISTINCT ON (filter_id)
    filter_id,
    order_value
  FROM requested
  WHERE filter_id >= 2
  ORDER BY filter_id, order_value
)
UPDATE dialog_filters f
SET order_value = deduped.order_value,
    updated_at = now()
FROM deduped
WHERE f.user_id = sqlc.arg(user_id)::bigint
  AND f.filter_id = deduped.filter_id;

-- name: GetDialogFolderTags :one
SELECT tags_enabled
FROM dialog_filter_settings
WHERE user_id = $1;

-- name: SetDialogFolderTags :exec
INSERT INTO dialog_filter_settings (
  user_id,
  tags_enabled
) VALUES (
  $1,
  $2
)
ON CONFLICT (user_id) DO UPDATE SET
  tags_enabled = EXCLUDED.tags_enabled,
  updated_at = now();

-- name: UpsertDialogDraft :exec
INSERT INTO dialog_drafts (
  user_id,
  peer_type,
  peer_id,
  top_message_id,
  date,
  draft
) VALUES (
  $1,
  $2,
  $3,
  $4,
  $5,
  sqlc.arg(draft_json)::jsonb
)
ON CONFLICT (user_id, peer_type, peer_id, top_message_id) DO UPDATE SET
  date = EXCLUDED.date,
  draft = EXCLUDED.draft,
  updated_at = now();

-- name: DeleteDialogDraft :one
WITH deleted AS (
  DELETE FROM dialog_drafts
  WHERE user_id = $1
    AND peer_type = $2
    AND peer_id = $3
    AND top_message_id = $4
  RETURNING user_id
)
SELECT EXISTS (SELECT 1 FROM deleted)::boolean AS changed;

-- name: ListDialogDrafts :many
SELECT draft::text AS draft_json
FROM dialog_drafts
WHERE user_id = $1
ORDER BY date DESC, peer_type ASC, peer_id DESC, top_message_id DESC
LIMIT sqlc.arg(limit_count);

-- name: ClearDialogDrafts :many
WITH doomed AS (
  SELECT d.user_id, d.peer_type, d.peer_id, d.top_message_id
  FROM dialog_drafts d
  WHERE d.user_id = $1
  ORDER BY d.date DESC, d.peer_type ASC, d.peer_id DESC, d.top_message_id DESC
  LIMIT sqlc.arg(limit_count)
),
deleted AS (
  DELETE FROM dialog_drafts d
  USING doomed
  WHERE d.user_id = doomed.user_id
    AND d.peer_type = doomed.peer_type
    AND d.peer_id = doomed.peer_id
    AND d.top_message_id = doomed.top_message_id
  RETURNING d.draft::text AS draft_json
)
SELECT draft_json
FROM deleted;

-- name: AdvanceDialogReadInboxFloor :exec
UPDATE dialogs
SET read_inbox_max_id = GREATEST(read_inbox_max_id, @read_inbox_max_id::int),
    updated_at = now()
WHERE user_id = @user_id
  AND peer_type = @peer_type
  AND peer_id = @peer_id;
