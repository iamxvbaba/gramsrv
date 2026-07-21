UPDATE public.bots
SET commands = COALESCE((
      SELECT jsonb_agg(command ORDER BY ordinal)
      FROM jsonb_array_elements(commands) WITH ORDINALITY AS item(command, ordinal)
      WHERE command->>'command' <> 'done'
    ), '[]'::jsonb),
    updated_at = now()
WHERE bot_user_id = 93372553;

UPDATE public.users
SET bot_info_version = bot_info_version + 1,
    updated_at = now()
WHERE id = 93372553;
