-- name: CreateBotWidget :exec
INSERT INTO bot_widgets (widget_id, bot_user_id, html, payload, width, height, allow_queries, public_data, public_data_hash, cache_time)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: CreateBotWidgetIfAbsent :execrows
INSERT INTO bot_widgets (widget_id, bot_user_id, html, payload, width, height, allow_queries, public_data, public_data_hash, cache_time)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (widget_id) DO NOTHING;

-- name: GetBotWidget :one
SELECT widget_id, bot_user_id, html, payload, width, height, allow_queries, public_data, public_data_hash, cache_time
FROM bot_widgets
WHERE widget_id = $1;

-- name: SetBotWidgetPublicData :execrows
UPDATE bot_widgets
SET public_data = $2, public_data_hash = $3, cache_time = $4, updated_at = now()
WHERE widget_id = $1;
