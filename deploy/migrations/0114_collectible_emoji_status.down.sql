DROP TRIGGER IF EXISTS unique_star_gifts_clear_invalid_emoji_status ON public.unique_star_gifts;
DROP FUNCTION IF EXISTS public.telesrv_clear_invalid_collectible_emoji_status();

UPDATE public.documents d
SET attributes = COALESCE((
  SELECT jsonb_agg(
    CASE
      WHEN item->>'kind' = 'custom_emoji' AND COALESCE((item->>'text_color')::boolean, false) THEN
        (item - 'text_color') || jsonb_build_object('kind', 'sticker')
      ELSE item
    END
    ORDER BY ord
  )
  FROM jsonb_array_elements(d.attributes) WITH ORDINALITY AS attrs(item, ord)
), '[]'::jsonb)
WHERE d.id IN (SELECT document_id FROM public.star_gift_collectible_patterns);

ALTER TABLE public.user_update_events DROP CONSTRAINT IF EXISTS user_update_events_type_check;
ALTER TABLE public.user_update_events ADD CONSTRAINT user_update_events_type_check CHECK (
  (event_type)::text = ANY (ARRAY[
    'new_message', 'read_history_inbox', 'read_history_outbox', 'read_message_contents',
    'edit_message', 'web_page', 'message_reactions', 'message_poll', 'draft_message', 'quick_replies',
    'new_quick_reply', 'delete_quick_reply', 'quick_reply_message', 'delete_quick_reply_messages',
    'contacts_reset', 'dialog_pinned', 'pinned_dialogs', 'pinned_messages', 'dialog_unread_mark',
    'peer_settings', 'peer_story_blocked', 'user_phone', 'delete_messages', 'dialog_filter',
    'dialog_filter_order', 'dialog_filters', 'folder_peers', 'channel_available_messages',
    'channel_view_forum_as_messages', 'channel_state', 'saved_dialog_pinned',
    'pinned_saved_dialogs', 'story', 'read_stories', 'sent_story_reaction',
    'new_story_reaction', 'noop', 'read_channel_discussion_inbox',
    'read_channel_discussion_outbox'
  ]::text[])
);
ALTER TABLE public.user_update_events DROP COLUMN IF EXISTS emoji_status_payload;

ALTER TABLE public.users DROP CONSTRAINT IF EXISTS users_deletion_state_check;
ALTER TABLE public.users DROP CONSTRAINT IF EXISTS users_emoji_status_shape_check;
ALTER TABLE public.users
  DROP COLUMN IF EXISTS emoji_status_collectible,
  DROP COLUMN IF EXISTS emoji_status_collectible_id;

ALTER TABLE public.users
  ADD CONSTRAINT users_deletion_state_check CHECK (
    (deleted_at IS NULL AND deletion_source = '' AND deletion_reason = '')
    OR
    (deleted_at IS NOT NULL
      AND deletion_source IN (
        'manual', 'forgot_password', 'tos_decline', 'password_reset_expiry',
        'account_ttl', 'freeze_expiry'
      )
      AND account_delete_at IS NULL
      AND phone = '' AND first_name = '' AND last_name = '' AND username = ''
      AND country_code = '' AND about = '' AND verified = false AND support = false
      AND premium_expires_at IS NULL
      AND emoji_status_document_id = 0 AND emoji_status_until = 0
      AND color_set = false AND color = 0 AND color_background_emoji_id = 0
      AND profile_color_set = false AND profile_color = 0
      AND profile_color_background_emoji_id = 0
      AND birthday_day = 0 AND birthday_month = 0 AND birthday_year = 0
      AND personal_channel_id = 0 AND last_seen_at = 0
      AND octet_length(deletion_reason) <= 1024)
  );
