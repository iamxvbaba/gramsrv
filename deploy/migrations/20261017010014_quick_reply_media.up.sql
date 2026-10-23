-- Quick reply messages may carry media (photo, video, file, album) and can be
-- edited, like in Telegram Business.
ALTER TABLE public.quick_reply_messages
    ADD COLUMN media jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN grouped_id bigint NOT NULL DEFAULT 0,
    ADD COLUMN edit_date integer NOT NULL DEFAULT 0;
