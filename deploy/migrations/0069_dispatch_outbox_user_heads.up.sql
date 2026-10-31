-- Claim only needs to look at each user's current unfinished head. Persisting the
-- head lowers claim complexity from "scan the entire outbox backlog and DISTINCT ON"
-- to "scan the user lanes that have a backlog".
-- Concurrent writes are blocked during the migration, so there is no gap between
-- the backfill and the trigger installed afterwards.
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

    -- Deleting a non-head needs no recompute. Deleting a head uses the
    -- (target_user_id, pts, id) index to find the next one; a failed head also
    -- blocks until it is explicitly deleted.
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

-- Claim no longer reads the expression shard index; remove it to avoid duplicate write amplification on every enqueue.
DROP INDEX IF EXISTS dispatch_outbox_logical_shard_head_idx;
