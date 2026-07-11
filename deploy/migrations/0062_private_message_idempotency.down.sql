ALTER TABLE private_messages
    DROP COLUMN IF EXISTS recipient_delivered,
    DROP COLUMN IF EXISTS request_fingerprint;
