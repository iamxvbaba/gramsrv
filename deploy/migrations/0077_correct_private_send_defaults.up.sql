-- 0062 and 0068 originally removed their expand-phase defaults immediately.
-- Databases that already applied those revisions therefore reject INSERTs from
-- an older telesrv process during a rolling deployment.  Restore the permanent
-- legacy-writer sentinels; fresh databases receive the same defaults directly
-- from the corrected original migrations.
ALTER TABLE public.private_messages
    ALTER COLUMN request_fingerprint SET DEFAULT '\x'::bytea,
    ALTER COLUMN recipient_delivered SET DEFAULT false,
    ALTER COLUMN sender_box_id SET DEFAULT 0,
    ALTER COLUMN sender_pts SET DEFAULT 0,
    ALTER COLUMN recipient_box_id SET DEFAULT 0,
    ALTER COLUMN recipient_pts SET DEFAULT 0;

-- Empty fingerprint and zero receipt fields mean "unknown legacy send".  They
-- are deliberately not backfilled: the immutable first response cannot be
-- reconstructed from message_boxes after edit/delete.  Replay must reject or
-- fail fast through the store invariants instead of normalizing these values.
