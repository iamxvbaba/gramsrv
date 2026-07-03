UPDATE message_boxes
SET hide_edited = false
WHERE message_sender_id = 1250000007
  AND from_user_id = 1250000007
  AND edit_date > 0
  AND hide_edited;

UPDATE private_messages
SET hide_edited = false
WHERE sender_user_id = 1250000007
  AND edit_date > 0
  AND hide_edited;
