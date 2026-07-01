DELETE FROM public.bot_chat_states
WHERE bot_user_id = 1063110917;

DELETE FROM public.bots
WHERE bot_user_id = 1063110917;

DELETE FROM public.peer_usernames
WHERE peer_type = 'user' AND peer_id = 1063110917;

DELETE FROM public.read_model_versions
WHERE owner_user_id = 1063110917 AND peer_type = 'user' AND peer_id = 1063110917;

DELETE FROM public.users
WHERE id = 1063110917;
