-- name: UpsertPushToken :exec
INSERT INTO device_push_tokens (user_id, token_type, token, secret, app_sandbox, no_muted, other_uids)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(token_type),
    sqlc.arg(token),
    sqlc.arg(secret),
    sqlc.arg(app_sandbox),
    sqlc.arg(no_muted),
    COALESCE(sqlc.arg(other_uids)::bigint[], '{}'::bigint[])
)
ON CONFLICT (token_type, token) DO UPDATE
SET user_id = EXCLUDED.user_id,
    secret = EXCLUDED.secret,
    app_sandbox = EXCLUDED.app_sandbox,
    no_muted = EXCLUDED.no_muted,
    other_uids = EXCLUDED.other_uids,
    updated_at = now();

-- name: DeletePushToken :execrows
DELETE FROM device_push_tokens
WHERE user_id = $1 AND token_type = $2 AND token = $3;

-- name: DeletePushTokenByValue :execrows
DELETE FROM device_push_tokens
WHERE token_type = $1 AND token = $2;

-- name: ListPushTokensForUser :many
SELECT user_id, token_type, token, secret, app_sandbox, no_muted, other_uids, updated_at
FROM device_push_tokens
WHERE user_id = $1
ORDER BY token_type, token;

-- name: ListPushTokensForUsers :many
SELECT user_id, token_type, token, secret, app_sandbox, no_muted, other_uids, updated_at
FROM device_push_tokens
WHERE user_id = ANY($1::bigint[])
ORDER BY user_id, token_type, token;
