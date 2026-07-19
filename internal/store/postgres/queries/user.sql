-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUsersByIDs :many
SELECT *
FROM users
WHERE id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY id;

-- name: GetUserByPhone :one
SELECT * FROM users WHERE phone = $1 AND deleted_at IS NULL;

-- name: GetUsersByPhones :many
SELECT *
FROM users
WHERE phone = ANY(sqlc.arg(phones)::text[]) AND deleted_at IS NULL
ORDER BY id;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE lower(username) = lower($1) AND username <> '' AND deleted_at IS NULL;

-- name: SearchUsers :many
WITH matched AS (
  SELECT
    u.id,
    u.access_hash,
    COALESCE(NULLIF(c.contact_phone, ''), u.phone)::text AS phone,
    COALESCE(NULLIF(c.contact_first_name, ''), u.first_name)::text AS first_name,
    COALESCE(c.contact_last_name, u.last_name)::text AS last_name,
    u.about,
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
    u.color_set,
    u.color,
    u.color_background_emoji_id,
    u.profile_color_set,
    u.profile_color,
    u.profile_color_background_emoji_id,
    u.last_seen_at,
    (c.contact_user_id IS NOT NULL)::boolean AS contact,
    COALESCE(c.mutual, false)::boolean AS mutual,
    CASE
      WHEN sqlc.arg(phone_query)::text <> '' AND u.phone = sqlc.arg(phone_query)::text THEN 0
      WHEN lower(u.username) = sqlc.arg(query_lower)::text THEN 1
      WHEN lower(COALESCE(NULLIF(c.contact_first_name, ''), u.first_name)) = sqlc.arg(query_lower)::text THEN 2
      WHEN lower(u.first_name) = sqlc.arg(query_lower)::text THEN 3
      WHEN c.contact_user_id IS NOT NULL THEN 4
      ELSE 5
    END AS rank
  FROM users u
  LEFT JOIN contacts c ON c.user_id = sqlc.arg(current_user_id)::bigint AND c.contact_user_id = u.id
  WHERE u.id <> sqlc.arg(current_user_id)::bigint
    AND u.deleted_at IS NULL
    AND sqlc.arg(query_lower)::text <> ''
    AND (
      (sqlc.arg(phone_query)::text <> '' AND u.phone LIKE sqlc.arg(phone_query)::text || '%')
      OR lower(u.username) LIKE sqlc.arg(query_like)::text || '%' ESCAPE '\'
      OR lower(u.first_name) LIKE '%' || sqlc.arg(query_like)::text || '%' ESCAPE '\'
      OR lower(u.last_name) LIKE '%' || sqlc.arg(query_like)::text || '%' ESCAPE '\'
      OR lower(trim(u.first_name || ' ' || u.last_name)) LIKE '%' || sqlc.arg(query_like)::text || '%' ESCAPE '\'
      OR lower(c.contact_first_name) LIKE '%' || sqlc.arg(query_like)::text || '%' ESCAPE '\'
      OR lower(c.contact_last_name) LIKE '%' || sqlc.arg(query_like)::text || '%' ESCAPE '\'
      OR lower(trim(c.contact_first_name || ' ' || c.contact_last_name)) LIKE '%' || sqlc.arg(query_like)::text || '%' ESCAPE '\'
    )
)
SELECT
  id,
  access_hash,
  phone,
  first_name,
  last_name,
  about,
  username,
  country_code,
  verified,
  support,
  is_bot,
  bot_info_version,
  premium_expires_at,
  emoji_status_document_id,
  emoji_status_until,
  emoji_status_collectible_id,
  emoji_status_collectible,
  color_set,
  color,
  color_background_emoji_id,
  profile_color_set,
  profile_color,
  profile_color_background_emoji_id,
  last_seen_at,
  contact,
  mutual
FROM matched
ORDER BY contact DESC, rank, id
LIMIT sqlc.arg(limit_count);

-- name: CreateUser :one
INSERT INTO users (access_hash, phone, first_name, last_name, username, country_code, premium_expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateUserUsername :one
UPDATE users
SET username = $2,
    updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: UpdateUserLastSeen :exec
UPDATE users
SET last_seen_at = GREATEST(last_seen_at, sqlc.arg(last_seen_at)::bigint),
    updated_at = now()
WHERE id = sqlc.arg(id)::bigint AND deleted_at IS NULL;

-- name: UpdateUserProfile :one
UPDATE users
SET first_name = $2,
    last_name = $3,
    about = $4,
    updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: UpdateUserPhone :one
UPDATE users
SET phone = sqlc.arg(phone)::text,
    updated_at = now()
WHERE id = sqlc.arg(id)::bigint AND deleted_at IS NULL
RETURNING *;

-- name: SetUserPremiumUntil :one
UPDATE users
SET premium_expires_at = sqlc.narg(premium_expires_at)::timestamptz,
    updated_at = now()
WHERE id = sqlc.arg(id)::bigint AND deleted_at IS NULL
RETURNING *;

-- name: SetUserVerified :one
UPDATE users
SET verified = sqlc.arg(verified)::boolean,
    updated_at = now()
WHERE id = sqlc.arg(id)::bigint AND deleted_at IS NULL
RETURNING *;

-- name: SweepExpiredPremium :many
UPDATE users
SET premium_expires_at = NULL,
    updated_at = now()
WHERE id IN (
  SELECT id FROM users
  WHERE premium_expires_at IS NOT NULL
    AND deleted_at IS NULL
    AND premium_expires_at <= sqlc.arg(now)::timestamptz
  ORDER BY premium_expires_at
  LIMIT sqlc.arg(limit_count)::int
)
RETURNING *;

-- name: UpdateUserEmojiStatus :one
UPDATE users
SET emoji_status_document_id = sqlc.arg(emoji_status_document_id)::bigint,
    emoji_status_until = sqlc.arg(emoji_status_until)::bigint,
    emoji_status_collectible_id = sqlc.narg(emoji_status_collectible_id)::bigint,
    emoji_status_collectible = sqlc.arg(emoji_status_collectible)::jsonb,
    updated_at = now()
WHERE id = sqlc.arg(id)::bigint AND deleted_at IS NULL
RETURNING *;

-- name: UpdateUserBirthday :one
UPDATE users
SET birthday_day = sqlc.arg(birthday_day)::int,
    birthday_month = sqlc.arg(birthday_month)::int,
    birthday_year = sqlc.arg(birthday_year)::int,
    updated_at = now()
WHERE id = sqlc.arg(id)::bigint AND deleted_at IS NULL
RETURNING *;

-- name: UpdateUserPersonalChannel :one
UPDATE users
SET personal_channel_id = sqlc.arg(personal_channel_id)::bigint,
    updated_at = now()
WHERE id = sqlc.arg(id)::bigint AND deleted_at IS NULL
RETURNING *;

-- name: UpdateUserColor :one
UPDATE users
SET color_set = sqlc.arg(color_set)::boolean,
    color = sqlc.arg(color)::int,
    color_background_emoji_id = sqlc.arg(background_emoji_id)::bigint,
    updated_at = now()
WHERE id = sqlc.arg(id)::bigint AND deleted_at IS NULL
RETURNING *;

-- name: UpdateUserProfileColor :one
UPDATE users
SET profile_color_set = sqlc.arg(color_set)::boolean,
    profile_color = sqlc.arg(color)::int,
    profile_color_background_emoji_id = sqlc.arg(background_emoji_id)::bigint,
    updated_at = now()
WHERE id = sqlc.arg(id)::bigint AND deleted_at IS NULL
RETURNING *;
