-- Fix: forum topic read-state (0002/a180fbc) added new user_update_events event types
-- read_channel_discussion_inbox / read_channel_discussion_outbox（updateReadChannelDiscussionInbox/
-- (durable multi-device sync via the outbox), but 0001's
-- user_update_events_type_check allowlist did not include them.
-- Consequence: on real PostgreSQL, when messages.readDiscussion first marks a
-- topic read, the appended durable event is rejected by the CHECK constraint
-- (SQLSTATE 23514) -> the RPC returns 500, and the topic read-state never enters
-- durable / getDifference, so the user's other devices never receive the
-- per-topic read state. The memory store has no CHECK constraint, so unit tests
-- did not expose this; it only surfaced with two real machines.
ALTER TABLE public.user_update_events DROP CONSTRAINT IF EXISTS user_update_events_type_check;
ALTER TABLE public.user_update_events ADD CONSTRAINT user_update_events_type_check CHECK (
    (event_type)::text = ANY (ARRAY[
        'new_message', 'read_history_inbox', 'read_history_outbox', 'read_message_contents',
        'edit_message', 'message_reactions', 'message_poll', 'draft_message', 'quick_replies',
        'new_quick_reply', 'delete_quick_reply', 'quick_reply_message', 'delete_quick_reply_messages',
        'contacts_reset', 'dialog_pinned', 'pinned_dialogs', 'pinned_messages', 'dialog_unread_mark',
        'peer_settings', 'peer_story_blocked', 'delete_messages', 'dialog_filter', 'dialog_filter_order',
        'dialog_filters', 'folder_peers', 'channel_view_forum_as_messages',
        'channel_state', 'saved_dialog_pinned', 'pinned_saved_dialogs', 'story', 'read_stories',
        'sent_story_reaction', 'new_story_reaction', 'noop',
        'read_channel_discussion_inbox', 'read_channel_discussion_outbox'
    ]::text[])
);
