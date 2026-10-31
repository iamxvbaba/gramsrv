ALTER TABLE private_messages
    ADD COLUMN request_fingerprint bytea NOT NULL DEFAULT '\x',
    ADD COLUMN recipient_delivered boolean NOT NULL DEFAULT false;

-- Old rows did not save the original request fingerprint, so an empty fingerprint
-- is kept, making later replays explicitly return RANDOM_ID_DUPLICATE; guessing or
-- repairing the original request from a possibly-edited message projection is
-- forbidden. recipient_delivered can be backfilled exactly from the same private-chat
-- fact table, distinguishing "never delivered because blocked" from "claimed
-- delivered but recipient box is missing".
UPDATE private_messages AS p
SET recipient_delivered = true
WHERE p.sender_user_id <> p.recipient_user_id
  AND EXISTS (
    SELECT 1
    FROM message_boxes AS b
    WHERE b.private_message_id = p.id
      AND b.owner_user_id = p.recipient_user_id
  );

-- Keep these defaults after the expand step.  A pre-0062 process does not name
-- either column in INSERT, so dropping them while that process can still serve
-- traffic turns an otherwise compatible rolling deployment into a NOT NULL
-- failure.  The empty fingerprint is an explicit "unknown legacy receipt"
-- sentinel: the new replay path rejects it as RANDOM_ID_DUPLICATE before it can
-- interpret recipient_delivered.  It must never be reconstructed from mutable
-- message_boxes.
