-- Excluding the originating device requires the exact physical
-- (raw auth_key_id, session_id) tuple. Install the guard first so no new
-- half-pair can race the explicit cleanup of legacy invalid online tasks.
DO $$
BEGIN
    -- Fresh databases already receive this constraint from 0001_init; upgraded
    -- databases do not. Keep one migration stream valid for both shapes.
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'dispatch_outbox'::regclass
          AND conname = 'dispatch_outbox_exclusion_pair_check'
    ) THEN
        ALTER TABLE dispatch_outbox
            ADD CONSTRAINT dispatch_outbox_exclusion_pair_check
            CHECK (
                (exclude_auth_key_id = 0 AND exclude_session_id = 0)
                OR
                (exclude_auth_key_id <> 0 AND exclude_session_id <> 0)
            ) NOT VALID;
    END IF;
END
$$;

-- dispatch_outbox is only an online delivery task queue. Its durable source
-- remains user_update_events through the existing (target_user_id, pts) FK,
-- and the delete trigger promotes the next per-user head. Missing tuple parts
-- cannot be reconstructed safely, so remove rather than normalize bad rows.
DELETE FROM dispatch_outbox
WHERE (exclude_auth_key_id = 0) <> (exclude_session_id = 0);

ALTER TABLE dispatch_outbox
    VALIDATE CONSTRAINT dispatch_outbox_exclusion_pair_check;
