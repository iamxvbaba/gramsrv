ALTER TABLE public.channel_messages
    DROP CONSTRAINT IF EXISTS channel_messages_delete_ids_array,
    DROP CONSTRAINT IF EXISTS channel_messages_send_snapshot_object,
    DROP COLUMN IF EXISTS delete_message_ids,
    DROP COLUMN IF EXISTS delete_date,
    DROP COLUMN IF EXISTS delete_pts_count,
    DROP COLUMN IF EXISTS delete_pts,
    DROP COLUMN IF EXISTS send_snapshot;

ALTER TABLE public.private_messages
    DROP CONSTRAINT IF EXISTS private_messages_sender_delete_ids_array,
    DROP CONSTRAINT IF EXISTS private_messages_sender_snapshot_object,
    DROP COLUMN IF EXISTS sender_delete_message_ids,
    DROP COLUMN IF EXISTS sender_delete_date,
    DROP COLUMN IF EXISTS sender_delete_pts_count,
    DROP COLUMN IF EXISTS sender_delete_pts,
    DROP COLUMN IF EXISTS sender_snapshot;
