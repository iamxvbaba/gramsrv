ALTER TABLE public.channel_messages
    ADD COLUMN paid_message_stars bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT channel_messages_paid_message_stars_check CHECK (paid_message_stars >= 0);
