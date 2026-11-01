-- A durable user update may have only one online delivery task. The historical
-- schema's ON CONFLICT DO NOTHING had no matching unique key, so the earliest task
-- is explicitly kept and duplicates removed first.
WITH duplicates AS (
    SELECT id
    FROM (
        SELECT
            id,
            row_number() OVER (
                PARTITION BY target_user_id, pts
                -- If historical duplicates still contain a deliverable task, keep
                -- it preferentially; an earlier failed copy must not overwrite a
                -- healthy pending copy and artificially block that user's lane.
                ORDER BY
                    CASE status
                        WHEN 'pending' THEN 0
                        WHEN 'dispatching' THEN 1
                        ELSE 2
                    END,
                    id ASC
            ) AS rn
        FROM dispatch_outbox
    ) ranked
    WHERE ranked.rn > 1
)
DELETE FROM dispatch_outbox d
USING duplicates x
WHERE d.id = x.id;

CREATE UNIQUE INDEX dispatch_outbox_user_pts_uidx
    ON dispatch_outbox (target_user_id, pts);

-- ClaimDispatchOutboxShards fixes 256 logical shards; the expression must match the
-- queries exactly, so each worker doesn't full-scan the table to filter its own lane.
CREATE INDEX dispatch_outbox_logical_shard_head_idx
    ON dispatch_outbox (
        mod(target_user_id, 256::bigint),
        target_user_id,
        pts,
        id
    );
