-- Complete collectible emoji-status state: users persist the selected unique
-- gift plus an immutable render snapshot, and the account update log persists
-- the exact status payload for online dispatch/offline difference replay.
ALTER TABLE public.users
  ADD COLUMN emoji_status_collectible_id bigint REFERENCES public.unique_star_gifts(id),
  ADD COLUMN emoji_status_collectible jsonb DEFAULT '{}'::jsonb NOT NULL;

ALTER TABLE public.users
  ADD CONSTRAINT users_emoji_status_shape_check CHECK (
    emoji_status_document_id >= 0
    AND emoji_status_until >= 0
    AND (emoji_status_document_id > 0 OR emoji_status_until = 0)
    AND (
      (
        emoji_status_collectible_id IS NULL
        AND emoji_status_collectible = '{}'::jsonb
      ) OR (
        emoji_status_collectible_id IS NOT NULL
        AND emoji_status_collectible_id > 0
        AND emoji_status_document_id > 0
        AND jsonb_typeof(emoji_status_collectible) = 'object'
        AND emoji_status_collectible <> '{}'::jsonb
        AND emoji_status_collectible ? 'collectible_id'
        AND emoji_status_collectible ? 'document_id'
        AND emoji_status_collectible ? 'title'
        AND emoji_status_collectible ? 'slug'
        AND emoji_status_collectible ? 'pattern_document_id'
        AND (emoji_status_collectible->>'collectible_id')::bigint = emoji_status_collectible_id
        AND (emoji_status_collectible->>'document_id')::bigint = emoji_status_document_id
        AND (emoji_status_collectible->>'pattern_document_id')::bigint > 0
        AND length(emoji_status_collectible->>'title') > 0
        AND length(emoji_status_collectible->>'slug') > 0
        AND (emoji_status_collectible->>'center_color')::integer BETWEEN 0 AND 16777215
        AND (emoji_status_collectible->>'edge_color')::integer BETWEEN 0 AND 16777215
        AND (emoji_status_collectible->>'pattern_color')::integer BETWEEN 0 AND 16777215
        AND (emoji_status_collectible->>'text_color')::integer BETWEEN 0 AND 16777215
      )
    )
  );

ALTER TABLE public.user_update_events
  ADD COLUMN emoji_status_payload jsonb DEFAULT '{}'::jsonb NOT NULL;

ALTER TABLE public.user_update_events DROP CONSTRAINT IF EXISTS user_update_events_type_check;
ALTER TABLE public.user_update_events ADD CONSTRAINT user_update_events_type_check CHECK (
  (event_type)::text = ANY (ARRAY[
    'new_message', 'read_history_inbox', 'read_history_outbox', 'read_message_contents',
    'edit_message', 'web_page', 'message_reactions', 'message_poll', 'draft_message', 'quick_replies',
    'new_quick_reply', 'delete_quick_reply', 'quick_reply_message', 'delete_quick_reply_messages',
    'contacts_reset', 'dialog_pinned', 'pinned_dialogs', 'pinned_messages', 'dialog_unread_mark',
    'peer_settings', 'peer_story_blocked', 'user_phone', 'user_emoji_status', 'delete_messages',
    'dialog_filter', 'dialog_filter_order', 'dialog_filters', 'folder_peers',
    'channel_available_messages', 'channel_view_forum_as_messages', 'channel_state',
    'saved_dialog_pinned', 'pinned_saved_dialogs', 'story', 'read_stories',
    'sent_story_reaction', 'new_story_reaction', 'noop',
    'read_channel_discussion_inbox', 'read_channel_discussion_outbox'
  ]::text[])
);

-- Android applies the collectible backdrop's pattern_color only to a
-- documentAttributeCustomEmoji with text_color=true.  Existing imports stored
-- these pattern documents as ordinary stickers, so repair them in place.
UPDATE public.documents d
SET attributes = COALESCE((
  SELECT jsonb_agg(
    CASE
      WHEN item->>'kind' = 'sticker' THEN
        jsonb_set(
          jsonb_set(item, '{kind}', '"custom_emoji"'::jsonb, false),
          '{text_color}', 'true'::jsonb, true
        )
      ELSE item
    END
    ORDER BY ord
  )
  FROM jsonb_array_elements(d.attributes) WITH ORDINALITY AS attrs(item, ord)
), '[]'::jsonb)
WHERE d.id IN (SELECT document_id FROM public.star_gift_collectible_patterns);

-- A transferred/exported/burned gift can no longer remain as the previous
-- owner's status.  Keep the durable user state valid even if the lifecycle
-- mutation did not originate from an account RPC.
CREATE OR REPLACE FUNCTION public.telesrv_clear_invalid_collectible_emoji_status()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  cleared_user_id bigint;
  cleared_pts integer;
  event_date integer;
BEGIN
  event_date := EXTRACT(EPOCH FROM clock_timestamp())::integer;
  FOR cleared_user_id IN
    UPDATE public.users u
       SET emoji_status_document_id = 0,
           emoji_status_until = 0,
           emoji_status_collectible_id = NULL,
           emoji_status_collectible = '{}'::jsonb,
           updated_at = now()
     WHERE u.emoji_status_collectible_id = NEW.id
       AND (
         NEW.burned
         OR NEW.owner_address <> ''
         OR NEW.owner_peer_type IS DISTINCT FROM 'user'
         OR NEW.owner_peer_id IS DISTINCT FROM u.id
       )
     RETURNING u.id
  LOOP
    INSERT INTO public.user_update_watermarks (user_id, contiguous_pts)
    VALUES (cleared_user_id, 0)
    ON CONFLICT (user_id) DO NOTHING;

    UPDATE public.user_update_watermarks
       SET contiguous_pts = contiguous_pts + 1,
           updated_at = now()
     WHERE user_id = cleared_user_id
     RETURNING contiguous_pts INTO cleared_pts;

    INSERT INTO public.user_update_events (
      user_id, pts, pts_count, date, event_type,
      peer_type, peer_id, emoji_status_payload
    ) VALUES (
      cleared_user_id, cleared_pts, 1, event_date, 'user_emoji_status',
      'user', cleared_user_id, '{}'::jsonb
    );

    -- No session is the origin of a lifecycle invalidation: every online
    -- device receives it, and offline devices replay the same event.
    INSERT INTO public.dispatch_outbox (
      target_user_id, pts, event_type, exclude_auth_key_id, exclude_session_id
    ) VALUES (
      cleared_user_id, cleared_pts, 'user_emoji_status', 0, 0
    );
  END LOOP;
  RETURN NEW;
END
$$;

CREATE TRIGGER unique_star_gifts_clear_invalid_emoji_status
AFTER UPDATE OF owner_peer_type, owner_peer_id, owner_address, burned
ON public.unique_star_gifts
FOR EACH ROW EXECUTE FUNCTION public.telesrv_clear_invalid_collectible_emoji_status();

-- Deleted-user tombstones must not retain the new collectible facts.
ALTER TABLE public.users DROP CONSTRAINT IF EXISTS users_deletion_state_check;
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
      AND emoji_status_collectible_id IS NULL
      AND emoji_status_collectible = '{}'::jsonb
      AND color_set = false AND color = 0 AND color_background_emoji_id = 0
      AND profile_color_set = false AND profile_color = 0
      AND profile_color_background_emoji_id = 0
      AND birthday_day = 0 AND birthday_month = 0 AND birthday_year = 0
      AND personal_channel_id = 0 AND last_seen_at = 0
      AND octet_length(deletion_reason) <= 1024)
  );
