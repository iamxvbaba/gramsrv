-- Rollback: remove 'channel_web_page' from the allowlist.
ALTER TABLE public.channel_update_events DROP CONSTRAINT IF EXISTS channel_update_events_type_check;
ALTER TABLE public.channel_update_events ADD CONSTRAINT channel_update_events_type_check CHECK (
    (event_type)::text = ANY (ARRAY[
        'new_channel_message', 'edit_channel_message', 'delete_channel_messages',
        'channel_participant', 'pinned_channel_messages', 'noop'
    ]::text[])
);
