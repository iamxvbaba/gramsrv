DROP TABLE IF EXISTS public.telegram_login_codes;
DROP TABLE IF EXISTS public.web_authorizations;
DROP TABLE IF EXISTS public.telegram_login_requests;
DROP TABLE IF EXISTS public.bot_login_native_apps;
DROP TABLE IF EXISTS public.bot_login_allowed_urls;
DROP TABLE IF EXISTS public.bot_login_clients;
UPDATE public.bots
SET commands = COALESCE((
      SELECT jsonb_agg(command ORDER BY ordinal)
      FROM jsonb_array_elements(commands) WITH ORDINALITY AS item(command, ordinal)
      WHERE command->>'command' NOT IN ('setlogin','logininfo','resetloginsecret')
    ), '[]'::jsonb),
    updated_at = now()
WHERE bot_user_id = 93372553;
