-- Privacy settings are absolute account state. Persist the immutable rule
-- snapshot next to the account pts event so other online sessions and offline
-- getDifference replay exactly the committed value without re-querying the
-- mutable account_privacy_rules row.
CREATE TABLE public.user_update_privacy_payloads (
  user_id bigint NOT NULL,
  pts integer NOT NULL CHECK (pts > 0),
  payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
  PRIMARY KEY (user_id, pts),
  CONSTRAINT user_update_privacy_payloads_event_fk
    FOREIGN KEY (user_id, pts)
    REFERENCES public.user_update_events(user_id, pts)
    ON DELETE CASCADE
);

ALTER TABLE public.user_update_events DROP CONSTRAINT IF EXISTS user_update_events_type_check;
ALTER TABLE public.user_update_events ADD CONSTRAINT user_update_events_type_check CHECK (
  (event_type)::text = ANY (ARRAY[
    'new_message', 'read_history_inbox', 'read_history_outbox', 'read_message_contents',
    'edit_message', 'web_page', 'message_reactions', 'message_poll', 'draft_message', 'quick_replies',
    'new_quick_reply', 'delete_quick_reply', 'quick_reply_message', 'delete_quick_reply_messages',
    'contacts_reset', 'dialog_pinned', 'pinned_dialogs', 'pinned_messages', 'dialog_unread_mark',
    'peer_settings', 'peer_story_blocked', 'user_phone', 'user_emoji_status', 'privacy', 'delete_messages',
    'dialog_filter', 'dialog_filter_order', 'dialog_filters', 'folder_peers',
    'channel_available_messages', 'channel_view_forum_as_messages', 'channel_state',
    'saved_dialog_pinned', 'pinned_saved_dialogs', 'story', 'read_stories',
    'sent_story_reaction', 'new_story_reaction', 'noop',
    'read_channel_discussion_inbox', 'read_channel_discussion_outbox'
  ]::text[])
);
