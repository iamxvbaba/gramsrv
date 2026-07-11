-- Contract rollback: only safe after every pre-0062/0068 writer has drained.
ALTER TABLE public.private_messages
    ALTER COLUMN request_fingerprint DROP DEFAULT,
    ALTER COLUMN recipient_delivered DROP DEFAULT,
    ALTER COLUMN sender_box_id DROP DEFAULT,
    ALTER COLUMN sender_pts DROP DEFAULT,
    ALTER COLUMN recipient_box_id DROP DEFAULT,
    ALTER COLUMN recipient_pts DROP DEFAULT;
