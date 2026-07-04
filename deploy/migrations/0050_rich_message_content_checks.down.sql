ALTER TABLE public.channel_messages
    DROP CONSTRAINT IF EXISTS channel_messages_content_check;

ALTER TABLE public.channel_messages
    ADD CONSTRAINT channel_messages_content_check
    CHECK (
        body <> ''::text
        OR action <> '{}'::jsonb
        OR media <> '{}'::jsonb
    );

ALTER TABLE public.private_messages
    DROP CONSTRAINT IF EXISTS private_messages_nonempty_body;

ALTER TABLE public.private_messages
    ADD CONSTRAINT private_messages_nonempty_body
    CHECK (
        body <> ''::text
        OR media <> '{}'::jsonb
    );
