-- 一条 durable user update 只能有一个在线投递任务。历史 schema 的
-- ON CONFLICT DO NOTHING 没有对应唯一键，先显式保留最早任务并删除重复项。
WITH duplicates AS (
    SELECT id
    FROM (
        SELECT
            id,
            row_number() OVER (
                PARTITION BY target_user_id, pts
                -- 若历史重复中仍有可投递任务，优先保留它；不能让一个更早的
                -- failed 副本覆盖健康 pending 副本并人为阻塞该用户 lane。
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

-- ClaimDispatchOutboxShards 固定用 256 logical shards；表达式必须与查询
-- 完全一致，避免每个 worker 为筛自己的 lane 全表扫描。
CREATE INDEX dispatch_outbox_logical_shard_head_idx
    ON dispatch_outbox (
        mod(target_user_id, 256::bigint),
        target_user_id,
        pts,
        id
    );
