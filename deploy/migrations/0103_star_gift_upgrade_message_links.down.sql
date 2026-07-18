-- Projection repair events are durable account history and are intentionally
-- not erased on rollback. Only remove the replay receipt column.
ALTER TABLE public.star_gift_upgrade_commands
    DROP CONSTRAINT IF EXISTS star_gift_upgrade_command_source_edit_pts_check,
    DROP COLUMN IF EXISTS source_edit_pts;
