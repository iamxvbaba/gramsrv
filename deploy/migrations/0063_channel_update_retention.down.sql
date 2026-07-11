-- Once a retained floor advances, channel_update_events below it are physically gone.  Dropping
-- the checkpoint would make an old client pts look like an ordinary empty difference and silently
-- lose the required channelDifferenceTooLong recovery boundary.  Refuse that irreversible down.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.channel_update_checkpoints
        WHERE retained_through_pts > 0
    ) THEN
        RAISE EXCEPTION
            'cannot roll back channel update retention: retained floor has advanced'
            USING ERRCODE = '55000';
    END IF;
END
$$;

DROP INDEX IF EXISTS public.channel_update_events_retention_seek_idx;
DROP TABLE IF EXISTS public.channel_update_checkpoints;
