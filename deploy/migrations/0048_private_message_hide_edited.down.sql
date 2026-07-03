ALTER TABLE message_boxes
    DROP COLUMN IF EXISTS hide_edited;

ALTER TABLE private_messages
    DROP COLUMN IF EXISTS hide_edited;
