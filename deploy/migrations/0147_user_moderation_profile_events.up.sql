-- SCAM/FAKE is viewer-visible absolute profile state. Each affected direct
-- contact/dialog viewer receives a durable account-pts refresh so online
-- outbox delivery and offline getDifference converge even when TDesktop keeps
-- its local contact/dialog cache across restarts.
ALTER TABLE public.user_update_events DROP CONSTRAINT IF EXISTS user_update_events_type_check;
ALTER TABLE public.user_update_events ADD CONSTRAINT user_update_events_type_check CHECK (
  (event_type)::text = ANY (ARRAY[
    'new_message', 'read_history_inbox', 'read_history_outbox', 'read_message_contents',
    'edit_message', 'web_page', 'message_reactions', 'message_poll', 'draft_message', 'quick_replies',
    'new_quick_reply', 'delete_quick_reply', 'quick_reply_message', 'delete_quick_reply_messages',
    'contacts_reset', 'dialog_pinned', 'pinned_dialogs', 'pinned_messages', 'dialog_unread_mark',
    'peer_settings', 'peer_story_blocked', 'user_phone', 'user_emoji_status', 'user_profile', 'privacy',
    'delete_messages', 'dialog_filter', 'dialog_filter_order', 'dialog_filters', 'folder_peers',
    'channel_available_messages', 'channel_view_forum_as_messages', 'channel_state',
    'saved_dialog_pinned', 'pinned_saved_dialogs', 'story', 'read_stories',
    'sent_story_reaction', 'new_story_reaction', 'noop',
    'read_channel_discussion_inbox', 'read_channel_discussion_outbox'
  ]::text[])
);
