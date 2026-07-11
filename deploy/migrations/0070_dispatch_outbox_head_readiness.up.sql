-- Keep claim readiness on the one-row-per-user head itself. Otherwise PostgreSQL may
-- legally reorder the head/outbox join and start from every eligible backlog row,
-- reintroducing the full-backlog scan that 0069 is meant to remove.
LOCK TABLE dispatch_outbox IN SHARE ROW EXCLUSIVE MODE;

ALTER TABLE dispatch_outbox_user_heads
    ADD COLUMN status varchar(16) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'dispatching', 'failed')),
    ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

UPDATE dispatch_outbox_user_heads h
SET status = d.status,
    next_attempt_at = d.next_attempt_at,
    updated_at = d.updated_at
FROM dispatch_outbox d
WHERE d.target_user_id = h.target_user_id
  AND d.id = h.head_id;

CREATE OR REPLACE FUNCTION dispatch_outbox_maintain_user_head()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    removed_head bigint;
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO dispatch_outbox_user_heads (
            target_user_id, head_id, head_pts, status, next_attempt_at, updated_at
        ) VALUES (
            NEW.target_user_id, NEW.id, NEW.pts, NEW.status, NEW.next_attempt_at, NEW.updated_at
        )
        ON CONFLICT (target_user_id) DO UPDATE
        SET head_id = EXCLUDED.head_id,
            head_pts = EXCLUDED.head_pts,
            status = EXCLUDED.status,
            next_attempt_at = EXCLUDED.next_attempt_at,
            updated_at = EXCLUDED.updated_at
        WHERE (EXCLUDED.head_pts, EXCLUDED.head_id) <
              (dispatch_outbox_user_heads.head_pts, dispatch_outbox_user_heads.head_id);
        RETURN NULL;
    ELSIF TG_OP = 'UPDATE' THEN
        UPDATE dispatch_outbox_user_heads
        SET status = NEW.status,
            next_attempt_at = NEW.next_attempt_at,
            updated_at = NEW.updated_at
        WHERE target_user_id = NEW.target_user_id
          AND head_id = NEW.id;
        RETURN NULL;
    END IF;

    DELETE FROM dispatch_outbox_user_heads
    WHERE target_user_id = OLD.target_user_id
      AND head_id = OLD.id
    RETURNING head_id INTO removed_head;

    IF removed_head IS NOT NULL THEN
        INSERT INTO dispatch_outbox_user_heads (
            target_user_id, head_id, head_pts, status, next_attempt_at, updated_at
        )
        SELECT target_user_id, id, pts, status, next_attempt_at, updated_at
        FROM dispatch_outbox
        WHERE target_user_id = OLD.target_user_id
        ORDER BY pts ASC, id ASC
        LIMIT 1
        ON CONFLICT (target_user_id) DO UPDATE
        SET head_id = EXCLUDED.head_id,
            head_pts = EXCLUDED.head_pts,
            status = EXCLUDED.status,
            next_attempt_at = EXCLUDED.next_attempt_at,
            updated_at = EXCLUDED.updated_at
        WHERE (EXCLUDED.head_pts, EXCLUDED.head_id) <
              (dispatch_outbox_user_heads.head_pts, dispatch_outbox_user_heads.head_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER dispatch_outbox_update_user_head
AFTER UPDATE OF status, next_attempt_at, updated_at ON dispatch_outbox
FOR EACH ROW
EXECUTE FUNCTION dispatch_outbox_maintain_user_head();
