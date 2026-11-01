-- The server-side sent/constructed update_states.pts is not client confirmation: a
-- difference response may be lost in the network. observed_pts is advanced only by
-- the pts the client actually carries back on a later request (or a snapshot
-- baseline explicitly established by getState), and retention may use only this
-- watermark.
ALTER TABLE public.update_states
    ADD COLUMN observed_pts integer NOT NULL DEFAULT 0 CHECK (observed_pts >= 0);

-- Account-level durable update safe-prefix retention watermark.
-- It records only the contiguous prefix "confirmed explicitly by all currently
-- authorized devices"; TDesktop does not support updates.differenceTooLong, so this
-- table must never be used for arbitrary TTL hard trimming.
CREATE TABLE public.user_update_retention (
    user_id bigint PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    retained_through_pts integer NOT NULL DEFAULT 0 CHECK (retained_through_pts >= 0),
    retained_through_date integer NOT NULL DEFAULT 0 CHECK (retained_through_date >= 0),
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);

CREATE INDEX user_update_retention_updated_idx
    ON public.user_update_retention (updated_at, user_id);

-- retention first picks the globally oldest safe candidate by time, then deletes the contiguous prefix by (user_id, pts).
CREATE INDEX user_update_events_retention_global_idx
    ON public.user_update_events (date, user_id, pts);
