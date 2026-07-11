-- Lost-response random_id replay must be able to acknowledge the original send
-- after mutable message rows were edited or deleted. Keep the first sender echo
-- and the exact durable delete event alongside the idempotency key.
ALTER TABLE public.private_messages
    ADD COLUMN sender_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN sender_delete_pts integer NOT NULL DEFAULT 0 CHECK (sender_delete_pts >= 0),
    ADD COLUMN sender_delete_pts_count integer NOT NULL DEFAULT 0 CHECK (sender_delete_pts_count >= 0),
    ADD COLUMN sender_delete_date integer NOT NULL DEFAULT 0 CHECK (sender_delete_date >= 0),
    ADD COLUMN sender_delete_message_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT private_messages_sender_snapshot_object CHECK (jsonb_typeof(sender_snapshot) IS NOT DISTINCT FROM 'object'),
    ADD CONSTRAINT private_messages_sender_delete_ids_array CHECK (jsonb_typeof(sender_delete_message_ids) IS NOT DISTINCT FROM 'array');

ALTER TABLE public.channel_messages
    ADD COLUMN send_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN delete_pts integer NOT NULL DEFAULT 0 CHECK (delete_pts >= 0),
    ADD COLUMN delete_pts_count integer NOT NULL DEFAULT 0 CHECK (delete_pts_count >= 0),
    ADD COLUMN delete_date integer NOT NULL DEFAULT 0 CHECK (delete_date >= 0),
    ADD COLUMN delete_message_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT channel_messages_send_snapshot_object CHECK (jsonb_typeof(send_snapshot) IS NOT DISTINCT FROM 'object'),
    ADD CONSTRAINT channel_messages_delete_ids_array CHECK (jsonb_typeof(delete_message_ids) IS NOT DISTINCT FROM 'array');

-- Existing rows predate immutable replay snapshots. Leaving them as {} makes a
-- replay fail fast instead of guessing the first response from mutable state.
