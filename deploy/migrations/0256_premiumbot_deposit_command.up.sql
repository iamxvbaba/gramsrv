-- v2 number 0256; rewritten from main 0229_premiumbot_deposit_command (main 3a84827a, B7g), see docs/main-v2-sync.md.
-- Appends /deposit (crypto deposit address) to the built-in @premiumbot command menu so that the client's "/"
-- completion lists it.
--
-- v2 rewrite: main's 0229 targets bot 1250000017, but that ID is @gifbot in both main and v2;
-- @premiumbot is 1250000015. main also overwrites the whole command list and adds /claim, which v2 lacks.
-- Here we only append the missing /deposit to the default @premiumbot row, keeping the existing commands and order.

UPDATE public.bots
SET commands = commands || '[{"command": "deposit", "description": "получить адрес для крипто-депозита"}]'::jsonb,
    updated_at = now()
WHERE bot_user_id = 1250000015
  AND jsonb_typeof(commands) = 'array'
  AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(commands) AS c WHERE c->>'command' = 'deposit');

UPDATE public.users
SET bot_info_version = bot_info_version + 1,
    updated_at = now()
WHERE id = 1250000015;
