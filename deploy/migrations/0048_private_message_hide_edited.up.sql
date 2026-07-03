ALTER TABLE private_messages
    ADD COLUMN hide_edited boolean DEFAULT false NOT NULL;

ALTER TABLE message_boxes
    ADD COLUMN hide_edited boolean DEFAULT false NOT NULL;
