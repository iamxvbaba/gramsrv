UPDATE public.bots
SET commands = COALESCE((
        SELECT jsonb_agg(c ORDER BY ord)
        FROM jsonb_array_elements(commands) WITH ORDINALITY AS t(c, ord)
        WHERE c->>'command' <> 'deposit'
    ), '[]'::jsonb),
    updated_at = now()
WHERE bot_user_id = 1250000015
  AND jsonb_typeof(commands) = 'array';

UPDATE public.users
SET bot_info_version = bot_info_version + 1,
    updated_at = now()
WHERE id = 1250000015;
