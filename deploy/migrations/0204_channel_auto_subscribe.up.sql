-- Admin panel: mark a channel/supergroup for auto-subscribe. Adding one here
-- immediately joins every current real (non-bot, non-deleted, non-system)
-- user to it (see internal/app/autosubscribe.Service.Add), and every account
-- that signs up afterwards is joined the moment it's created (see
-- auth.Service.SignUp's post-creation hook). Removing a row only stops
-- FUTURE signups from joining -- it deliberately does not touch anyone
-- already a member, matching what was asked for.
CREATE TABLE public.channel_auto_subscribe (
    channel_id bigint NOT NULL,
    added_by text NOT NULL DEFAULT '',
    added_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_auto_subscribe_pkey PRIMARY KEY (channel_id),
    CONSTRAINT channel_auto_subscribe_channel_id_fkey FOREIGN KEY (channel_id)
        REFERENCES public.channels (id) ON DELETE CASCADE
);
