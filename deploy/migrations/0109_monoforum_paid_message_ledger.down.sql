ALTER TABLE public.channel_messages
    DROP CONSTRAINT IF EXISTS channel_messages_paid_message_stars_check,
    DROP COLUMN IF EXISTS paid_message_stars;
