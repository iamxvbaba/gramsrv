-- v2 number 0273; ported from main 20261106030000_channel_widget_data (main 6dd83b44, gramsrv#173), see docs/main-v2-sync.md.
-- Rollback: rewrite any retained channel_widget_data rows to noop so narrowing
-- the constraint cannot fail with SQLSTATE 23514 (and pts stays contiguous), then
-- remove 'channel_widget_data' from the allowlist.
UPDATE public.channel_update_events SET event_type = 'noop' WHERE event_type = 'channel_widget_data';
ALTER TABLE public.channel_update_events DROP CONSTRAINT IF EXISTS channel_update_events_type_check;
ALTER TABLE public.channel_update_events ADD CONSTRAINT channel_update_events_type_check CHECK (
    (event_type)::text = ANY (ARRAY[
        'new_channel_message', 'edit_channel_message', 'channel_web_page', 'delete_channel_messages',
        'channel_participant', 'pinned_channel_messages', 'noop'
    ]::text[])
);
