-- Remove the built-in @verifybot seed. Its private history is left alone: chat
-- rows reference the account, and dropping them would rewrite users' dialogs.
DELETE FROM public.read_model_versions
WHERE owner_user_id = 1250000011 AND peer_type = 'user' AND peer_id = 1250000011;

DELETE FROM public.peer_usernames
WHERE peer_type = 'user' AND peer_id = 1250000011;

DELETE FROM public.bots WHERE bot_user_id = 1250000011;
