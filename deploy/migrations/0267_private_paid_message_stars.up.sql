-- v2 编号 0267；移植自 main 0245_private_paid_message_stars（main 815612a9，gramsrv#152，B21），见 docs/main-v2-sync.md。
-- Private paid-message stars: shared logical row record + per-box display data.
ALTER TABLE public.private_messages
    ADD COLUMN paid_message_stars bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT private_messages_paid_message_stars_check CHECK (paid_message_stars >= 0);

ALTER TABLE public.message_boxes
    ADD COLUMN paid_message_stars bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT message_boxes_paid_message_stars_check CHECK (paid_message_stars >= 0);
