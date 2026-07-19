DELETE FROM public.bot_api_updates WHERE update_kind = 'callback_query';

DROP INDEX IF EXISTS public.bot_api_updates_callback_query_unique;
DROP INDEX IF EXISTS public.bot_api_updates_message_source_unique;

ALTER TABLE public.bot_api_updates
    DROP CONSTRAINT bot_api_updates_callback_shape_check,
    DROP CONSTRAINT bot_api_updates_kind_check;

ALTER TABLE public.bot_api_updates
    ADD CONSTRAINT bot_api_updates_kind_check
        CHECK ((update_kind)::text = ANY (ARRAY['message'::text, 'edited_message'::text])),
    ADD CONSTRAINT bot_api_updates_source_unique
        UNIQUE (bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts);

ALTER TABLE public.bot_api_updates
    DROP COLUMN callback_data,
    DROP COLUMN callback_chat_instance,
    DROP COLUMN callback_user_id,
    DROP COLUMN callback_query_id;
