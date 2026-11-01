-- Album group id: all messages of one messages.sendMultiMedia call share a
-- non-zero grouped_id, which clients use to render them as a single album group.
-- Previously sendMultiMedia did not bind grouped_id (each item was its own bubble).
-- Mirrors via_bot_id across the three tables: shared private-chat body + per-owner
-- inbox + channel messages.
ALTER TABLE public.private_messages ADD COLUMN grouped_id bigint DEFAULT 0 NOT NULL;
ALTER TABLE public.message_boxes ADD COLUMN grouped_id bigint DEFAULT 0 NOT NULL;
ALTER TABLE public.channel_messages ADD COLUMN grouped_id bigint DEFAULT 0 NOT NULL;
