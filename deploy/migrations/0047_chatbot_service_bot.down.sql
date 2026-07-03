DELETE FROM public.read_model_versions
WHERE owner_user_id = 1250000007
  AND peer_type = 'user'
  AND peer_id = 1250000007
  AND model IN ('contact_account', 'channel_active_memberships');

DELETE FROM public.peer_usernames
WHERE username_lower = 'chatbot'
  AND peer_type = 'user'
  AND peer_id = 1250000007;

DELETE FROM public.bots
WHERE bot_user_id = 1250000007
  AND owner_user_id = 1250000007;

DELETE FROM public.users
WHERE id = 1250000007
  AND username = 'ChatBot'
  AND is_bot = true;
