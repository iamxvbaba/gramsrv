ALTER TABLE public.bot_api_updates
    ADD COLUMN ephemeral_payload jsonb;

ALTER TABLE public.bot_api_updates
    ADD CONSTRAINT bot_api_updates_ephemeral_shape_check CHECK (
        ephemeral_payload IS NULL
        OR (
            peer_type = 'channel'
            AND peer_id > 0
            AND message_id > 0
            AND source_pts = 0
            AND jsonb_typeof(ephemeral_payload) = 'object'
            AND jsonb_typeof(ephemeral_payload -> 'Message') = 'object'
            AND (ephemeral_payload #>> '{Message,ID}') IS NOT NULL
            AND (ephemeral_payload #>> '{Message,Peer,Type}') IS NOT NULL
            AND (ephemeral_payload #>> '{Message,Peer,ID}') IS NOT NULL
            AND (ephemeral_payload #>> '{Message,SenderUserID}') IS NOT NULL
            AND (ephemeral_payload #>> '{Message,ReceiverUserID}') IS NOT NULL
            AND (ephemeral_payload #>> '{Message,Version}') IS NOT NULL
            AND NOT ((ephemeral_payload -> 'Message') ?| ARRAY[
                'RandomID', 'OriginDevice', 'PayloadHash', 'CreatedAt', 'Deleted'
            ])
            AND (
                NOT (ephemeral_payload ? 'ReplyTo')
                OR (
                    jsonb_typeof(ephemeral_payload -> 'ReplyTo') = 'object'
                    AND NOT ((ephemeral_payload -> 'ReplyTo') ?| ARRAY[
                        'RandomID', 'OriginDevice', 'PayloadHash', 'CreatedAt', 'Deleted'
                    ])
                )
            )
            AND (ephemeral_payload #>> '{Message,ID}')::integer = message_id
            AND (ephemeral_payload #>> '{Message,Peer,Type}') = peer_type
            AND (ephemeral_payload #>> '{Message,Peer,ID}')::bigint = peer_id
            AND (
                (update_kind = 'callback_query'
                    AND (ephemeral_payload #>> '{Message,SenderUserID}')::bigint = bot_user_id)
                OR
                (update_kind IN ('message', 'edited_message')
                    AND (ephemeral_payload #>> '{Message,ReceiverUserID}')::bigint = bot_user_id)
            )
            AND (ephemeral_payload #>> '{Message,Version}')::bigint > 0
        )
    );

DROP INDEX public.bot_api_updates_message_source_unique;

CREATE UNIQUE INDEX bot_api_updates_message_source_unique
    ON public.bot_api_updates (bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts)
    WHERE update_kind IN ('message', 'edited_message') AND ephemeral_payload IS NULL;

CREATE UNIQUE INDEX bot_api_updates_ephemeral_version_unique
    ON public.bot_api_updates (
        bot_user_id,
        update_kind,
        peer_type,
        peer_id,
        message_id,
        ((ephemeral_payload #>> '{Message,Version}')::bigint)
    )
    WHERE update_kind IN ('message', 'edited_message') AND ephemeral_payload IS NOT NULL;
