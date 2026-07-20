ALTER TABLE public.user_update_events
    DROP CONSTRAINT IF EXISTS user_update_events_peer_type_check;
ALTER TABLE public.user_update_events
    ADD CONSTRAINT user_update_events_peer_type_check
    CHECK (peer_type IS NULL OR peer_type IN ('user','channel'));

DELETE FROM public.notify_settings WHERE scope_kind='peer' AND peer_type='community';

DROP TRIGGER IF EXISTS users_linked_community_read_model_changed ON public.users;
DROP FUNCTION IF EXISTS public.telesrv_notify_user_linked_community_read_model();

DROP INDEX IF EXISTS public.channels_linked_community_idx;
DROP INDEX IF EXISTS public.users_linked_community_idx;
ALTER TABLE public.channels DROP COLUMN IF EXISTS linked_community_id;
ALTER TABLE public.users DROP COLUMN IF EXISTS linked_community_id;
DROP TABLE IF EXISTS public.community_user_states;
DROP TABLE IF EXISTS public.community_peer_link_requests;
DROP TABLE IF EXISTS public.community_peer_links;
DROP TABLE IF EXISTS public.community_members;
DROP TABLE IF EXISTS public.communities;
