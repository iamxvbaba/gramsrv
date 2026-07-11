ALTER TABLE public.private_messages
    DROP COLUMN IF EXISTS recipient_pts,
    DROP COLUMN IF EXISTS recipient_box_id,
    DROP COLUMN IF EXISTS sender_pts,
    DROP COLUMN IF EXISTS sender_box_id;
