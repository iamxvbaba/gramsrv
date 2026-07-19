ALTER TABLE channel_messages
    ADD COLUMN suggested_post jsonb NOT NULL DEFAULT '{}'::jsonb;

-- A monoforum is a virtual per-saved-peer container, never an ordinary joined megagroup.
DELETE FROM channel_dialogs d
USING channels c
WHERE d.channel_id = c.id AND c.monoforum;

DELETE FROM user_channel_member_index i
USING channels c
WHERE i.channel_id = c.id AND c.monoforum;

DELETE FROM channel_members m
USING channels c
WHERE m.channel_id = c.id AND c.monoforum;

UPDATE channels c
SET participants_count = 0,
    admins_count = 0,
    updated_at = now()
WHERE c.monoforum AND (c.participants_count <> 0 OR c.admins_count <> 0);

-- Older generic sends from non-admin users are deterministically their own saved-peer dialog.
UPDATE channel_messages m
SET saved_peer_type = 'user',
    saved_peer_id = m.sender_user_id
FROM channels mono
WHERE mono.id = m.channel_id
  AND mono.monoforum
  AND NOT m.deleted
  AND m.saved_peer_id = 0
  AND m.action = '{}'::jsonb
  AND m.sender_user_id <> 0
  AND NOT EXISTS (
      SELECT 1
      FROM channel_members parent_member
      WHERE parent_member.channel_id = mono.linked_monoforum_id
        AND parent_member.user_id = m.sender_user_id
        AND parent_member.status = 'active'
        AND parent_member.role IN ('creator', 'admin')
  );

UPDATE channel_update_events e
SET payload = jsonb_set(
        e.payload,
        '{message,SavedPeer}',
        jsonb_build_object('Type', 'user', 'ID', m.sender_user_id),
        true
    )
FROM channel_messages m, channels mono
WHERE mono.id = m.channel_id
  AND mono.monoforum
  AND e.channel_id = m.channel_id
  AND e.message_id = m.id
  AND m.saved_peer_type = 'user'
  AND m.saved_peer_id = m.sender_user_id
  AND COALESCE((e.payload #>> '{message,SavedPeer,ID}')::bigint, 0) = 0;

UPDATE channel_messages m
SET send_snapshot = jsonb_set(
        m.send_snapshot,
        '{message,SavedPeer}',
        jsonb_build_object('Type', 'user', 'ID', m.sender_user_id),
        true
    )
FROM channels mono
WHERE mono.id = m.channel_id
  AND mono.monoforum
  AND m.random_id <> 0
  AND m.saved_peer_type = 'user'
  AND m.saved_peer_id = m.sender_user_id
  AND COALESCE((m.send_snapshot #>> '{message,SavedPeer,ID}')::bigint, 0) = 0;

-- Remove the impossible join service message without creating a pts gap: retain the event row as noop.
UPDATE channel_update_events e
SET event_type = 'noop', message_id = 0, sender_user_id = 0, user_ids = '[]'::jsonb, payload = '{}'::jsonb
FROM channel_messages m, channels mono
WHERE mono.id = m.channel_id
  AND mono.monoforum
  AND e.channel_id = m.channel_id
  AND e.message_id = m.id
  AND m.action->>'Type' = 'chat_joined';

UPDATE channel_messages m
SET deleted = true
FROM channels mono
WHERE mono.id = m.channel_id
  AND mono.monoforum
  AND m.action->>'Type' = 'chat_joined';

UPDATE channels mono
SET top_message_id = COALESCE((
        SELECT max(m.id)
        FROM channel_messages m
        WHERE m.channel_id = mono.id AND NOT m.deleted
    ), 0),
    updated_at = now()
WHERE mono.monoforum;
