UPDATE private_messages
SET hide_edited = true
WHERE sender_user_id = 1250000007
  AND edit_date > 0
  AND NOT hide_edited;

UPDATE message_boxes
SET hide_edited = true
WHERE message_sender_id = 1250000007
  AND from_user_id = 1250000007
  AND edit_date > 0
  AND NOT hide_edited;
