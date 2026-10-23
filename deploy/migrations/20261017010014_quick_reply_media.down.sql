ALTER TABLE public.quick_reply_messages
    DROP COLUMN IF EXISTS media,
    DROP COLUMN IF EXISTS grouped_id,
    DROP COLUMN IF EXISTS edit_date;
