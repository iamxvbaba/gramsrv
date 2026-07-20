DELETE FROM public.bot_api_updates
WHERE ephemeral_payload IS NOT NULL;

DROP INDEX IF EXISTS public.bot_api_updates_ephemeral_version_unique;
DROP INDEX IF EXISTS public.bot_api_updates_message_source_unique;

ALTER TABLE public.bot_api_updates
    DROP CONSTRAINT bot_api_updates_ephemeral_shape_check,
    DROP COLUMN ephemeral_payload;

CREATE UNIQUE INDEX bot_api_updates_message_source_unique
    ON public.bot_api_updates (bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts)
    WHERE update_kind IN ('message', 'edited_message');
