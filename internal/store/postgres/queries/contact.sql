-- name: ListContactsByUser :many
SELECT
  c.contact_user_id,
  c.mutual,
  c.close_friend,
  c.contact_phone,
  c.contact_first_name,
  c.contact_last_name,
  c.note,
  COALESCE(c.note_entities::text, '[]')::text AS note_entities_json,
  u.id,
  u.access_hash,
  COALESCE(NULLIF(c.contact_phone, ''), u.phone)::text AS phone,
  COALESCE(NULLIF(c.contact_first_name, ''), u.first_name)::text AS first_name,
  COALESCE(c.contact_last_name, u.last_name)::text AS last_name,
  u.username,
  u.country_code,
  u.verified,
  u.support,
  u.is_bot,
  u.bot_info_version,
  u.premium_expires_at,
  u.emoji_status_document_id,
  u.emoji_status_until,
  u.emoji_status_collectible_id,
  u.emoji_status_collectible,
  u.last_seen_at
FROM contacts c
JOIN users u ON u.id = c.contact_user_id
WHERE c.user_id = $1
ORDER BY c.contact_first_name, c.contact_last_name, u.first_name, u.last_name, u.id;

-- name: GetContact :one
SELECT
  c.contact_user_id,
  c.mutual,
  c.close_friend,
  c.contact_phone,
  c.contact_first_name,
  c.contact_last_name,
  c.note,
  COALESCE(c.note_entities::text, '[]')::text AS note_entities_json,
  u.id,
  u.access_hash,
  COALESCE(NULLIF(c.contact_phone, ''), u.phone)::text AS phone,
  COALESCE(NULLIF(c.contact_first_name, ''), u.first_name)::text AS first_name,
  COALESCE(c.contact_last_name, u.last_name)::text AS last_name,
  u.username,
  u.country_code,
  u.verified,
  u.support,
  u.is_bot,
  u.bot_info_version,
  u.premium_expires_at,
  u.emoji_status_document_id,
  u.emoji_status_until,
  u.emoji_status_collectible_id,
  u.emoji_status_collectible,
  u.last_seen_at
FROM contacts c
JOIN users u ON u.id = c.contact_user_id
WHERE c.user_id = $1
  AND c.contact_user_id = $2;

-- name: UpsertContact :one
WITH reverse AS (
  SELECT EXISTS (
    SELECT 1
    FROM contacts
    WHERE user_id = sqlc.arg(contact_user_id)::bigint
      AND contact_user_id = sqlc.arg(user_id)::bigint
  )::boolean AS mutual
),
upserted AS (
  INSERT INTO contacts (
    user_id,
    contact_user_id,
    contact_phone,
    contact_first_name,
    contact_last_name,
    note,
    note_entities,
    mutual
  )
  SELECT
    sqlc.arg(user_id)::bigint,
    sqlc.arg(contact_user_id)::bigint,
    sqlc.arg(contact_phone)::text,
    sqlc.arg(contact_first_name)::text,
    sqlc.arg(contact_last_name)::text,
    sqlc.arg(note)::text,
    sqlc.arg(note_entities)::jsonb,
    reverse.mutual
  FROM reverse
  ON CONFLICT (user_id, contact_user_id) DO UPDATE SET
    contact_phone = EXCLUDED.contact_phone,
    contact_first_name = EXCLUDED.contact_first_name,
    contact_last_name = EXCLUDED.contact_last_name,
    note = EXCLUDED.note,
    note_entities = EXCLUDED.note_entities,
    mutual = contacts.mutual OR EXCLUDED.mutual,
    updated_at = now()
  RETURNING *
),
reverse_updated AS (
  UPDATE contacts c
  SET mutual = true,
      updated_at = now()
  WHERE c.user_id = sqlc.arg(contact_user_id)::bigint
    AND c.contact_user_id = sqlc.arg(user_id)::bigint
    AND NOT c.mutual
  RETURNING c.user_id
)
SELECT
  c.contact_user_id,
  c.mutual,
  c.close_friend,
  c.contact_phone,
  c.contact_first_name,
  c.contact_last_name,
  c.note,
  COALESCE(c.note_entities::text, '[]')::text AS note_entities_json,
  u.id,
  u.access_hash,
  COALESCE(NULLIF(c.contact_phone, ''), u.phone)::text AS phone,
  COALESCE(NULLIF(c.contact_first_name, ''), u.first_name)::text AS first_name,
  COALESCE(c.contact_last_name, u.last_name)::text AS last_name,
  u.username,
  u.country_code,
  u.verified,
  u.support,
  u.is_bot,
  u.bot_info_version,
  u.premium_expires_at,
  u.emoji_status_document_id,
  u.emoji_status_until,
  u.emoji_status_collectible_id,
  u.emoji_status_collectible,
  u.last_seen_at,
  EXISTS (SELECT 1 FROM reverse_updated)::boolean AS reverse_mutual_changed
FROM upserted c
JOIN users u ON u.id = c.contact_user_id;

-- name: UpdateContactNote :one
WITH updated AS (
  UPDATE contacts c
  SET note = sqlc.arg(note)::text,
      note_entities = sqlc.arg(note_entities)::jsonb,
      updated_at = now()
  WHERE c.user_id = sqlc.arg(user_id)::bigint
    AND c.contact_user_id = sqlc.arg(contact_user_id)::bigint
  RETURNING *
)
SELECT
  c.contact_user_id,
  c.mutual,
  c.close_friend,
  c.contact_phone,
  c.contact_first_name,
  c.contact_last_name,
  c.note,
  COALESCE(c.note_entities::text, '[]')::text AS note_entities_json,
  u.id,
  u.access_hash,
  COALESCE(NULLIF(c.contact_phone, ''), u.phone)::text AS phone,
  COALESCE(NULLIF(c.contact_first_name, ''), u.first_name)::text AS first_name,
  COALESCE(c.contact_last_name, u.last_name)::text AS last_name,
  u.username,
  u.country_code,
  u.verified,
  u.support,
  u.is_bot,
  u.bot_info_version,
  u.premium_expires_at,
  u.emoji_status_document_id,
  u.emoji_status_until,
  u.emoji_status_collectible_id,
  u.emoji_status_collectible,
  u.last_seen_at
FROM updated c
JOIN users u ON u.id = c.contact_user_id;

-- name: DeleteContacts :one
WITH deleted AS (
  DELETE FROM contacts
  WHERE user_id = sqlc.arg(user_id)::bigint
    AND contact_user_id = ANY(sqlc.arg(contact_user_ids)::bigint[])
  RETURNING contact_user_id
),
reverse_updated AS (
  UPDATE contacts c
  SET mutual = false,
      updated_at = now()
  FROM deleted d
  WHERE c.user_id = d.contact_user_id
    AND c.contact_user_id = sqlc.arg(user_id)::bigint
  RETURNING c.user_id
)
SELECT COUNT(*)::int AS deleted_count
FROM deleted;
