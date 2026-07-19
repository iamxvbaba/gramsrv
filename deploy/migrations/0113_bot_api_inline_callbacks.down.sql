DELETE FROM public.bot_api_updates
WHERE update_kind = 'callback_query' AND callback_inline_message_id <> 0;

ALTER TABLE public.bot_api_updates
    DROP CONSTRAINT bot_api_updates_callback_shape_check,
    DROP CONSTRAINT bot_api_updates_peer_type_check,
    DROP CONSTRAINT bot_api_updates_peer_id_check,
    DROP CONSTRAINT bot_api_updates_message_id_check;

ALTER TABLE public.bot_api_updates
    ADD CONSTRAINT bot_api_updates_peer_type_check CHECK (peer_type IN ('user', 'channel')),
    ADD CONSTRAINT bot_api_updates_peer_id_check CHECK (peer_id > 0),
    ADD CONSTRAINT bot_api_updates_message_id_check CHECK (message_id > 0),
    ADD CONSTRAINT bot_api_updates_callback_shape_check CHECK (
        (update_kind = 'callback_query' AND callback_query_id <> 0 AND callback_user_id > 0
            AND callback_chat_instance <> 0 AND COALESCE(octet_length(callback_data), 0) <= 64
            AND source_pts = 0)
        OR
        (update_kind IN ('message', 'edited_message') AND callback_query_id = 0
            AND callback_user_id = 0 AND callback_chat_instance = 0 AND callback_data IS NULL)
    );

ALTER TABLE public.bot_api_updates
    DROP COLUMN callback_inline_access_hash,
    DROP COLUMN callback_inline_message_id,
    DROP COLUMN callback_inline_owner_id,
    DROP COLUMN callback_inline_dc_id;
