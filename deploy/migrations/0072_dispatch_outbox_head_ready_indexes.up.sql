-- Claim only eligible durable heads. Partial indexes keep work proportional to ready
-- user lanes instead of every account that has a deferred/failed backlog.
CREATE INDEX dispatch_outbox_user_heads_pending_shard_idx
    ON dispatch_outbox_user_heads (
        logical_shard,
        next_attempt_at,
        target_user_id,
        head_pts,
        head_id
    )
    WHERE status = 'pending';

CREATE INDEX dispatch_outbox_user_heads_dispatching_shard_idx
    ON dispatch_outbox_user_heads (
        logical_shard,
        updated_at,
        target_user_id,
        head_pts,
        head_id
    )
    WHERE status = 'dispatching';

-- A head must always reference the exact outbox row it represents. The base schema already has
-- dispatch_outbox_target_user_id_id_key on these columns; reuse it instead of maintaining a
-- duplicate unique index on every enqueue/delete. Deferred validation lets the AFTER DELETE
-- trigger promote/delete the head in the same tx.
ALTER TABLE dispatch_outbox_user_heads
    ADD CONSTRAINT dispatch_outbox_user_heads_outbox_fkey
    FOREIGN KEY (target_user_id, head_id)
    REFERENCES dispatch_outbox (target_user_id, id)
    DEFERRABLE INITIALLY DEFERRED;

DROP INDEX IF EXISTS dispatch_outbox_user_heads_shard_idx;
DROP INDEX IF EXISTS dispatch_outbox_pending_ready_idx;
DROP INDEX IF EXISTS dispatch_outbox_dispatching_stale_ready_idx;
