-- Remove the built-in @verifierbot seed. Its private history is left alone: chat
-- rows reference the account, and dropping them would rewrite users' dialogs.
--
-- Any verifier status an operator granted this bot lives in bot_verifier_settings
-- (0154) and is removed with that migration, not here: this file only undoes the
-- account seed it created.
DELETE FROM public.read_model_versions
WHERE owner_user_id = 1250000013 AND peer_type = 'user' AND peer_id = 1250000013;

DELETE FROM public.peer_usernames
WHERE peer_type = 'user' AND peer_id = 1250000013;

DELETE FROM public.bots WHERE bot_user_id = 1250000013;
