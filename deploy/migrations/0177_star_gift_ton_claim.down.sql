DROP TABLE IF EXISTS public.star_gift_claim_history;
DROP TABLE IF EXISTS public.star_gift_claim_challenges;

UPDATE public.unique_star_gifts
SET host_peer_type=NULL, host_peer_id=NULL, updated_at=now()
WHERE host_peer_type='user' AND host_peer_id IN (1250000019,1250000021);

DELETE FROM public.peer_usernames WHERE username_lower IN ('relayer','claim')
  AND peer_type='user' AND peer_id IN (1250000019,1250000021);
DELETE FROM public.read_model_versions WHERE owner_user_id IN (1250000019,1250000021);
DELETE FROM public.bots WHERE bot_user_id=1250000021;
DELETE FROM public.users WHERE id IN (1250000019,1250000021);
