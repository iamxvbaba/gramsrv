CREATE INDEX IF NOT EXISTS dispatch_outbox_pending_ready_idx
    ON dispatch_outbox (next_attempt_at, target_user_id, pts, id)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS dispatch_outbox_dispatching_stale_ready_idx
    ON dispatch_outbox (updated_at, target_user_id, pts, id)
    WHERE status = 'dispatching';

CREATE INDEX IF NOT EXISTS dispatch_outbox_user_heads_shard_idx
    ON dispatch_outbox_user_heads (logical_shard, target_user_id);

ALTER TABLE dispatch_outbox_user_heads
    DROP CONSTRAINT IF EXISTS dispatch_outbox_user_heads_outbox_fkey;

DROP INDEX IF EXISTS dispatch_outbox_user_heads_dispatching_shard_idx;
DROP INDEX IF EXISTS dispatch_outbox_user_heads_pending_shard_idx;
