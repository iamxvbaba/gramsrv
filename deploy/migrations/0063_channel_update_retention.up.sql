-- Channel-scoped update retention checkpoint.
--
-- channel_update_events may now be pruned in bounded per-channel transactions. The checkpoint is
-- the durable protocol boundary: request pts below retained_through_pts must receive
-- updates.channelDifferenceTooLong; latest_event_date/latest_pts remain after row deletion so an
-- account-level updates.getDifference can still emit UpdateChannelTooLong for an offline member.
CREATE TABLE IF NOT EXISTS public.channel_update_checkpoints (
    channel_id bigint PRIMARY KEY REFERENCES public.channels(id) ON DELETE CASCADE,
    retained_through_pts integer NOT NULL DEFAULT 0 CHECK (retained_through_pts >= 0),
    latest_event_date integer NOT NULL DEFAULT 0 CHECK (latest_event_date >= 0),
    latest_pts integer NOT NULL DEFAULT 0 CHECK (latest_pts >= 0),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT channel_update_checkpoints_floor_check CHECK (retained_through_pts <= latest_pts)
);

-- Backfill every existing channel, including channels without an event row, so read paths can use
-- the checkpoint directly without a full event-log fallback.
INSERT INTO public.channel_update_checkpoints (
    channel_id, retained_through_pts, latest_event_date, latest_pts
)
SELECT c.id,
       0,
       COALESCE(MAX(e.date), 0)::integer,
       c.pts
FROM public.channels c
LEFT JOIN public.channel_update_events e ON e.channel_id = c.id
GROUP BY c.id, c.pts
ON CONFLICT (channel_id) DO UPDATE SET
    latest_event_date = GREATEST(channel_update_checkpoints.latest_event_date, EXCLUDED.latest_event_date),
    latest_pts = GREATEST(channel_update_checkpoints.latest_pts, EXCLUDED.latest_pts),
    updated_at = now();

-- Global retention candidate seek. Exact per-channel deletion continues to use PK(channel_id,pts).
CREATE INDEX IF NOT EXISTS channel_update_events_retention_seek_idx
    ON public.channel_update_events (date, channel_id, pts);
