-- Private message box ids are account-local. Repair user-owned Star Gift
-- service actions that copied the owner's msg_id into both participants'
-- message boxes, and publish durable edit_message facts for already-visible
-- incorrect projections.

CREATE TEMP TABLE star_gift_box_media_repairs (
    owner_user_id bigint NOT NULL,
    box_id integer NOT NULL,
    peer_type text NOT NULL,
    peer_id bigint NOT NULL,
    repaired_media jsonb NOT NULL,
    PRIMARY KEY (owner_user_id, box_id)
) ON COMMIT DROP;

-- An upgrade action points back to the original ordinary gift. user saved_id
-- is a management identity, not a conversation message link: only the current
-- gift owner's box may carry it. The other participant must omit the field.
INSERT INTO star_gift_box_media_repairs (
    owner_user_id, box_id, peer_type, peer_id, repaired_media
)
SELECT unique_box.owner_user_id,
       unique_box.box_id,
       unique_box.peer_type,
       unique_box.peer_id,
       CASE
           WHEN unique_box.owner_user_id = gift.owner_peer_id THEN jsonb_set(
               unique_box.media,
               '{service_action,star_gift_unique,saved_id}',
               to_jsonb(gift.msg_id::bigint),
               true
           )
           ELSE unique_box.media #- '{service_action,star_gift_unique,saved_id}'
       END
FROM peer_star_gifts gift
JOIN message_boxes upgrade_owner
  ON upgrade_owner.owner_user_id = gift.owner_peer_id
 AND upgrade_owner.box_id = gift.upgrade_msg_id
JOIN message_boxes unique_box
  ON unique_box.message_sender_id = upgrade_owner.message_sender_id
 AND unique_box.private_message_id = upgrade_owner.private_message_id
WHERE gift.owner_peer_type = 'user'
  AND gift.unique_gift_id IS NOT NULL
  AND gift.msg_id > 0
  AND gift.upgrade_msg_id > 0
  AND NOT unique_box.deleted
  AND unique_box.media #>> '{service_action,kind}' = 'star_gift_unique'
  AND unique_box.media #>> '{service_action,star_gift_unique,upgrade}' = 'true'
  AND unique_box.media IS DISTINCT FROM CASE
      WHEN unique_box.owner_user_id = gift.owner_peer_id THEN jsonb_set(
          unique_box.media,
          '{service_action,star_gift_unique,saved_id}',
          to_jsonb(gift.msg_id::bigint),
          true
      )
      ELSE unique_box.media #- '{service_action,star_gift_unique,saved_id}'
  END
ON CONFLICT (owner_user_id, box_id) DO UPDATE
SET repaired_media = EXCLUDED.repaired_media;

-- For every other user-target unique action (transfer, resale, offer accept,
-- craft), the action message itself is the new user saved-gift identity.
-- saved_id is a channel-only field there and must be absent from every box.
INSERT INTO star_gift_box_media_repairs (
    owner_user_id, box_id, peer_type, peer_id, repaired_media
)
SELECT box.owner_user_id,
       box.box_id,
       box.peer_type,
       box.peer_id,
       box.media #- '{service_action,star_gift_unique,saved_id}'
FROM message_boxes box
WHERE NOT box.deleted
  AND box.media #>> '{service_action,kind}' = 'star_gift_unique'
  AND box.media #>> '{service_action,star_gift_unique,peer,Type}' = 'user'
  AND COALESCE((box.media #>> '{service_action,star_gift_unique,upgrade}')::boolean, false) = false
  AND box.media #> '{service_action,star_gift_unique,saved_id}' IS NOT NULL
ON CONFLICT (owner_user_id, box_id) DO UPDATE
SET repaired_media = EXCLUDED.repaired_media;

-- A separate prepaid-upgrade action points to the same ordinary gift.
-- Telegram defines gift_msg_id as receiver-only, so retain it only in the
-- owner's service-message box and remove it from the payer's outgoing copy.
INSERT INTO star_gift_box_media_repairs (
    owner_user_id, box_id, peer_type, peer_id, repaired_media
)
SELECT prepay_box.owner_user_id,
       prepay_box.box_id,
       prepay_box.peer_type,
       prepay_box.peer_id,
       CASE
           WHEN prepay_box.owner_user_id = gift.owner_peer_id THEN jsonb_set(
               prepay_box.media,
               '{service_action,star_gift,gift_msg_id}',
               to_jsonb(gift.msg_id::bigint),
               true
           )
           ELSE prepay_box.media #- '{service_action,star_gift,gift_msg_id}'
       END
FROM peer_star_gifts gift
JOIN message_boxes prepay_owner
  ON prepay_owner.owner_user_id = gift.owner_peer_id
 AND prepay_owner.media #>> '{service_action,kind}' = 'star_gift'
 AND prepay_owner.media #>> '{service_action,star_gift,prepaid_upgrade}' = 'true'
 AND prepay_owner.media #>> '{service_action,star_gift,upgrade_separate}' = 'true'
 AND (prepay_owner.media #>> '{service_action,star_gift,gift_msg_id}')::integer = gift.msg_id
 AND (prepay_owner.media #>> '{service_action,star_gift,gift_id}')::bigint = gift.gift_id
JOIN message_boxes prepay_box
  ON prepay_box.message_sender_id = prepay_owner.message_sender_id
 AND prepay_box.private_message_id = prepay_owner.private_message_id
WHERE gift.owner_peer_type = 'user'
  AND gift.msg_id > 0
  AND NOT prepay_box.deleted
  AND prepay_box.media IS DISTINCT FROM CASE
      WHEN prepay_box.owner_user_id = gift.owner_peer_id THEN jsonb_set(
          prepay_box.media,
          '{service_action,star_gift,gift_msg_id}',
          to_jsonb(gift.msg_id::bigint),
          true
      )
      ELSE prepay_box.media #- '{service_action,star_gift,gift_msg_id}'
  END
ON CONFLICT (owner_user_id, box_id) DO UPDATE
SET repaired_media = EXCLUDED.repaired_media;

DO $$
DECLARE
    repair record;
    next_pts integer;
    event_date integer := EXTRACT(EPOCH FROM clock_timestamp())::integer;
BEGIN
    FOR repair IN
        SELECT owner_user_id, box_id, peer_type, peer_id, repaired_media
        FROM star_gift_box_media_repairs
        ORDER BY owner_user_id, box_id
    LOOP
        INSERT INTO user_update_watermarks (user_id, contiguous_pts)
        VALUES (repair.owner_user_id, 0)
        ON CONFLICT (user_id) DO NOTHING;

        UPDATE user_update_watermarks
        SET contiguous_pts = contiguous_pts + 1,
            updated_at = now()
        WHERE user_id = repair.owner_user_id
        RETURNING contiguous_pts INTO next_pts;

        UPDATE message_boxes
        SET media = repair.repaired_media,
            pts = next_pts
        WHERE owner_user_id = repair.owner_user_id
          AND box_id = repair.box_id
          AND NOT deleted;

        INSERT INTO user_update_events (
            user_id, pts, pts_count, date, event_type,
            message_box_id, peer_type, peer_id
        ) VALUES (
            repair.owner_user_id, next_pts, 1, event_date, 'edit_message',
            repair.box_id, repair.peer_type, repair.peer_id
        );

        INSERT INTO dispatch_outbox (
            target_user_id, pts, event_type,
            exclude_auth_key_id, exclude_session_id
        ) VALUES (repair.owner_user_id, next_pts, 'edit_message', 0, 0);
    END LOOP;
END
$$;

-- private_messages is the logical shared envelope and cannot contain either
-- participant's local message id. User-visible history/difference always reads
-- the per-owner message_boxes snapshots repaired above.
UPDATE private_messages
SET media = media
    #- '{service_action,star_gift,saved_id}'
    #- '{service_action,star_gift,gift_msg_id}'
    #- '{service_action,star_gift,upgrade_msg_id}'
WHERE media #>> '{service_action,kind}' = 'star_gift'
  AND (
      media #> '{service_action,star_gift,peer_user_id}' IS NOT NULL
      OR media #>> '{service_action,star_gift,to,Type}' = 'user'
  );

UPDATE private_messages
SET media = media #- '{service_action,star_gift_unique,saved_id}'
WHERE media #>> '{service_action,kind}' = 'star_gift_unique'
  AND media #>> '{service_action,star_gift_unique,peer,Type}' = 'user';
