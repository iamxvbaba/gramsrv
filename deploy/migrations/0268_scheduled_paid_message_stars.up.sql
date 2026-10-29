-- v2 编号 0268；移植自 main 0246_scheduled_paid_message_stars（main 815612a9，gramsrv#152，B21），见 docs/main-v2-sync.md。
-- Client-agreed paid-message allowance captured when a message is scheduled.
ALTER TABLE public.scheduled_messages
    ADD COLUMN paid_stars bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT scheduled_messages_paid_stars_check CHECK (paid_stars >= 0);
