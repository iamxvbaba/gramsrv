-- messages.editMessageWidgetData adds the channel_update_events event type
-- 'channel_widget_data': for a channel message it patches the widget's
-- public/chat data in place and appends a durable channel event (same shape as
-- the existing 'channel_web_page'). The 0001/0016 allowlist does not include it,
-- so on real Postgres the append is rejected by the CHECK constraint (SQLSTATE
-- 23514); the memory store has no constraint, so unit tests did not expose it.
ALTER TABLE public.channel_update_events DROP CONSTRAINT IF EXISTS channel_update_events_type_check;
ALTER TABLE public.channel_update_events ADD CONSTRAINT channel_update_events_type_check CHECK (
    (event_type)::text = ANY (ARRAY[
        'new_channel_message', 'edit_channel_message', 'channel_web_page',
        'channel_widget_data', 'delete_channel_messages',
        'channel_participant', 'pinned_channel_messages', 'noop'
    ]::text[])
);
