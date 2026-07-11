CREATE INDEX IF NOT EXISTS dispatch_outbox_logical_shard_head_idx
    ON dispatch_outbox (
        mod(target_user_id, 256::bigint),
        target_user_id,
        pts,
        id
    );

DROP TRIGGER IF EXISTS dispatch_outbox_delete_user_head ON dispatch_outbox;
DROP TRIGGER IF EXISTS dispatch_outbox_insert_user_head ON dispatch_outbox;
DROP FUNCTION IF EXISTS dispatch_outbox_maintain_user_head();
DROP TABLE IF EXISTS dispatch_outbox_user_heads;
