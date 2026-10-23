-- The default product identity is now ShuzaGram (@shuzagram). Clients already
-- see the branded system account 777000 through the RPC projection, but
-- username resolution reads peer_usernames, which the initial schema seeded
-- with 'telesrv'. Move only the untouched seed, and only if the new username
-- is free, so @shuzagram resolves to the system account.
UPDATE public.peer_usernames
SET username_lower = 'shuzagram', username = 'shuzagram', updated_at = now()
WHERE peer_type = 'user' AND peer_id = 777000 AND username_lower = 'telesrv'
  AND NOT EXISTS (SELECT 1 FROM public.peer_usernames WHERE username_lower = 'shuzagram')
  AND NOT EXISTS (SELECT 1 FROM public.users WHERE id <> 777000 AND lower(username) = 'shuzagram');

UPDATE public.users
SET username = 'shuzagram', first_name = 'ShuzaGram', updated_at = now()
WHERE id = 777000 AND username = 'telesrv'
  AND EXISTS (SELECT 1 FROM public.peer_usernames
              WHERE peer_type = 'user' AND peer_id = 777000 AND username_lower = 'shuzagram');
