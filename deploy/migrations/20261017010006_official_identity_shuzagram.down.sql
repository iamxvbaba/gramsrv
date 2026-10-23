UPDATE public.peer_usernames
SET username_lower = 'telesrv', username = 'telesrv', updated_at = now()
WHERE peer_type = 'user' AND peer_id = 777000 AND username_lower = 'shuzagram'
  AND NOT EXISTS (SELECT 1 FROM public.peer_usernames WHERE username_lower = 'telesrv');

UPDATE public.users
SET username = 'telesrv', first_name = 'Telesrv', updated_at = now()
WHERE id = 777000 AND username = 'shuzagram'
  AND EXISTS (SELECT 1 FROM public.peer_usernames
              WHERE peer_type = 'user' AND peer_id = 777000 AND username_lower = 'telesrv');
