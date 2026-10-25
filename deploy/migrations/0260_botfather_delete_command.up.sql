-- v2 编号 0260；移植自 main 0236_botfather_delete_command（main c34bc584，B9），见 docs/main-v2-sync.md。
-- Repair the published BotFather menu on existing databases as well as fresh
-- installs. Bump its client cache version only when the menu actually changes.
WITH rewritten AS (
    SELECT bot_user_id, (
        SELECT COALESCE(jsonb_agg(
            CASE item.command->>'command'
                WHEN 'mybots' THEN jsonb_set(item.command, '{description}', '"edit your bots"'::jsonb)
                WHEN 'token' THEN jsonb_set(item.command, '{description}', '"get authorization token"'::jsonb)
                WHEN 'revoke' THEN jsonb_set(item.command, '{description}', '"revoke bot access token"'::jsonb)
                WHEN 'deletebot' THEN jsonb_set(item.command, '{description}', '"delete a bot"'::jsonb)
                ELSE item.command
            END ORDER BY item.ordinal), '[]'::jsonb)
        FROM jsonb_array_elements(commands) WITH ORDINALITY AS item(command, ordinal)
    ) AS commands
    FROM public.bots WHERE bot_user_id = 93372553
), expected AS (
    SELECT bot_user_id, commands || CASE
        WHEN commands @> '[{"command":"deletebot"}]'::jsonb THEN '[]'::jsonb
        ELSE '[{"command":"deletebot","description":"delete a bot"}]'::jsonb
    END AS commands FROM rewritten
), changed AS (
    UPDATE public.bots b SET commands = e.commands, updated_at = now()
    FROM expected e
    WHERE b.bot_user_id = e.bot_user_id AND b.commands IS DISTINCT FROM e.commands
    RETURNING b.bot_user_id
)
UPDATE public.users SET bot_info_version = bot_info_version + 1, updated_at = now()
WHERE id IN (SELECT bot_user_id FROM changed);
