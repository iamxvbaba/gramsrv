-- Claim 只需要查看每个用户当前未完成 head。把 head 持久化后，领取复杂度由
-- “扫描全部 outbox 积压并 DISTINCT ON”降为“扫描有积压的用户 lane”。
-- 迁移期间阻止并发写，保证 backfill 与随后安装的触发器之间没有缺口。
LOCK TABLE dispatch_outbox IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE dispatch_outbox_user_heads (
    target_user_id bigint PRIMARY KEY,
    head_id bigint NOT NULL,
    head_pts integer NOT NULL CHECK (head_pts >= 0),
    logical_shard smallint GENERATED ALWAYS AS (
        mod(target_user_id, 256::bigint)::smallint
    ) STORED,
    CHECK (logical_shard >= 0 AND logical_shard < 256)
);

CREATE INDEX dispatch_outbox_user_heads_shard_idx
    ON dispatch_outbox_user_heads (logical_shard, target_user_id);

INSERT INTO dispatch_outbox_user_heads (target_user_id, head_id, head_pts)
SELECT DISTINCT ON (target_user_id)
    target_user_id,
    id,
    pts
FROM dispatch_outbox
ORDER BY target_user_id ASC, pts ASC, id ASC;

CREATE FUNCTION dispatch_outbox_maintain_user_head()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    removed_head bigint;
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO dispatch_outbox_user_heads (target_user_id, head_id, head_pts)
        VALUES (NEW.target_user_id, NEW.id, NEW.pts)
        ON CONFLICT (target_user_id) DO UPDATE
        SET head_id = EXCLUDED.head_id,
            head_pts = EXCLUDED.head_pts
        WHERE (EXCLUDED.head_pts, EXCLUDED.head_id) <
              (dispatch_outbox_user_heads.head_pts, dispatch_outbox_user_heads.head_id);
        RETURN NULL;
    END IF;

    -- 删除非 head 不需要重算。删除 head 时用 (target_user_id, pts, id)
    -- 索引找下一条；failed head 同样会一直阻塞，直到被显式删除。
    DELETE FROM dispatch_outbox_user_heads
    WHERE target_user_id = OLD.target_user_id
      AND head_id = OLD.id
    RETURNING head_id INTO removed_head;

    IF removed_head IS NOT NULL THEN
        INSERT INTO dispatch_outbox_user_heads (target_user_id, head_id, head_pts)
        SELECT target_user_id, id, pts
        FROM dispatch_outbox
        WHERE target_user_id = OLD.target_user_id
        ORDER BY pts ASC, id ASC
        LIMIT 1
        ON CONFLICT (target_user_id) DO UPDATE
        SET head_id = EXCLUDED.head_id,
            head_pts = EXCLUDED.head_pts
        WHERE (EXCLUDED.head_pts, EXCLUDED.head_id) <
              (dispatch_outbox_user_heads.head_pts, dispatch_outbox_user_heads.head_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER dispatch_outbox_insert_user_head
AFTER INSERT ON dispatch_outbox
FOR EACH ROW
EXECUTE FUNCTION dispatch_outbox_maintain_user_head();

CREATE TRIGGER dispatch_outbox_delete_user_head
AFTER DELETE ON dispatch_outbox
FOR EACH ROW
EXECUTE FUNCTION dispatch_outbox_maintain_user_head();

-- Claim 已不再读取表达式 shard 索引；移除它避免每次 enqueue 的重复写放大。
DROP INDEX IF EXISTS dispatch_outbox_logical_shard_head_idx;
