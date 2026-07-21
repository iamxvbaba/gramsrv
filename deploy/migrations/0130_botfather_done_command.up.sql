-- /setlogin remains active across multiple configuration messages. Publish
-- /done in BotFather's command menu so clients can discover the explicit
-- finish action without reopening /help.
UPDATE public.bots
SET commands = commands || '[
      {"command":"done","description":"finish Telegram Login configuration"}
    ]'::jsonb,
    updated_at = now()
WHERE bot_user_id = 93372553
  AND NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(commands) AS item(command)
      WHERE item.command->>'command' = 'done'
  );

-- Bot command menus are cached by bot_info_version. Bump it even when an
-- operator already added /done manually, making the migration convergent and
-- forcing connected clients to refresh the authoritative command list.
UPDATE public.users
SET bot_info_version = bot_info_version + 1,
    updated_at = now()
WHERE id = 93372553;
