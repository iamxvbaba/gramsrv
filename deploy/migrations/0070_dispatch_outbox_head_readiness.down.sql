DROP TRIGGER IF EXISTS dispatch_outbox_update_user_head ON dispatch_outbox;

CREATE OR REPLACE FUNCTION dispatch_outbox_maintain_user_head()
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

ALTER TABLE dispatch_outbox_user_heads
    DROP COLUMN updated_at,
    DROP COLUMN next_attempt_at,
    DROP COLUMN status;
