ALTER TABLE public.bot_api_updates
    ADD COLUMN callback_query_id bigint NOT NULL DEFAULT 0,
    ADD COLUMN callback_user_id bigint NOT NULL DEFAULT 0,
    ADD COLUMN callback_chat_instance bigint NOT NULL DEFAULT 0,
    ADD COLUMN callback_data bytea;

ALTER TABLE public.bot_api_updates
    DROP CONSTRAINT bot_api_updates_kind_check,
    DROP CONSTRAINT bot_api_updates_source_unique;

ALTER TABLE public.bot_api_updates
    ADD CONSTRAINT bot_api_updates_kind_check
        CHECK ((update_kind)::text = ANY (ARRAY['message'::text, 'edited_message'::text, 'callback_query'::text])),
    ADD CONSTRAINT bot_api_updates_callback_shape_check CHECK (
        (update_kind = 'callback_query'
            AND callback_query_id <> 0
            AND callback_user_id > 0
            AND callback_chat_instance <> 0
            AND COALESCE(octet_length(callback_data), 0) <= 64
            AND source_pts = 0)
        OR
        (update_kind IN ('message', 'edited_message')
            AND callback_query_id = 0
            AND callback_user_id = 0
            AND callback_chat_instance = 0
            AND callback_data IS NULL)
    );

CREATE UNIQUE INDEX bot_api_updates_message_source_unique
    ON public.bot_api_updates (bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts)
    WHERE update_kind IN ('message', 'edited_message');

CREATE UNIQUE INDEX bot_api_updates_callback_query_unique
    ON public.bot_api_updates (bot_user_id, callback_query_id)
    WHERE update_kind = 'callback_query';
