ALTER TABLE public.channel_messages
    DROP CONSTRAINT IF EXISTS channel_messages_request_fingerprint_size,
    DROP COLUMN IF EXISTS request_fingerprint;
