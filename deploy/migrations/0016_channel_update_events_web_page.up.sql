-- Channel link-preview async backfill (updateChannelWebPage) adds the
-- channel_update_events event type 'channel_web_page': once a channel message's
-- pending link preview is resolved out of band, EditChannelMessage's WebPageResolve
-- mode replaces it in place and appends a channel_web_page durable event. The 0001
-- allowlist did not include it, so on real Postgres the append is rejected by the
-- CHECK constraint (SQLSTATE 23514); the memory store has no constraint, so unit
-- tests did not expose it (same lesson as 0015/0003).
ALTER TABLE public.channel_update_events DROP CONSTRAINT IF EXISTS channel_update_events_type_check;
ALTER TABLE public.channel_update_events ADD CONSTRAINT channel_update_events_type_check CHECK (
    (event_type)::text = ANY (ARRAY[
        'new_channel_message', 'edit_channel_message', 'channel_web_page', 'delete_channel_messages',
        'channel_participant', 'pinned_channel_messages', 'noop'
    ]::text[])
);
