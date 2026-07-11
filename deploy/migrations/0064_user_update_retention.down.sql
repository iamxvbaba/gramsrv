-- TDesktop has no account-level differenceTooLong fallback.  If any confirmed prefix has already
-- been deleted, removing this floor/observed state would turn a durable history hole into a false
-- empty difference.  A rollback is safe only before retention has advanced anywhere.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.user_update_retention
        WHERE retained_through_pts > 0
    ) THEN
        RAISE EXCEPTION
            'cannot roll back user update retention: retained floor has advanced'
            USING ERRCODE = '55000';
    END IF;
END
$$;

DROP INDEX IF EXISTS public.user_update_events_retention_global_idx;
DROP TABLE IF EXISTS public.user_update_retention;
ALTER TABLE public.update_states DROP COLUMN IF EXISTS observed_pts;
