-- A separate prepaid-upgrade service message is another owner-local entry to
-- the same saved-gift aggregate. Earlier writes persisted gift_msg_id in the
-- receiver projection but did not register that message id, so clients that
-- submitted the visible card id received STARGIFT_INVALID. If the gift was
-- upgraded through the original id, the prepaid card also remained actionable.
--
-- Repair aliases and already-upgraded projections atomically. Durable edit
-- events make history, online delivery and updates.getDifference converge on
-- the same non-actionable snapshot. Invalid persisted shapes fail the migration
-- instead of being normalized by a read path.

LOCK TABLE public.peer_star_gifts, public.star_gift_user_message_refs,
    public.message_boxes, public.private_messages IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.message_boxes box
        WHERE NOT box.deleted
          AND box.media #>> '{service_action,kind}' = 'star_gift'
          AND box.media #>> '{service_action,star_gift,prepaid_upgrade}' = 'true'
          AND box.media #>> '{service_action,star_gift,upgrade_separate}' = 'true'
          AND (
              jsonb_typeof(box.media #> '{service_action,star_gift,gift_id}') IS DISTINCT FROM 'number'
              OR COALESCE(box.media #>> '{service_action,star_gift,gift_id}', '') !~ '^[0-9]+$'
              OR (box.media #>> '{service_action,star_gift,gift_id}')::numeric <= 0
              OR (box.media #>> '{service_action,star_gift,gift_id}')::numeric > 9223372036854775807
          )
    ) THEN
        RAISE EXCEPTION 'separate prepaid star gift message has malformed gift_id';
    END IF;

    -- gift_msg_id is receiver-only, so absence is valid on the payer box. If
    -- present it must be a positive protocol int32 message id.
    IF EXISTS (
        SELECT 1
        FROM public.message_boxes box
        WHERE NOT box.deleted
          AND box.media #>> '{service_action,kind}' = 'star_gift'
          AND box.media #>> '{service_action,star_gift,prepaid_upgrade}' = 'true'
          AND box.media #>> '{service_action,star_gift,upgrade_separate}' = 'true'
          AND box.media #> '{service_action,star_gift,gift_msg_id}' IS NOT NULL
          AND (
              jsonb_typeof(box.media #> '{service_action,star_gift,gift_msg_id}') <> 'number'
              OR COALESCE(box.media #>> '{service_action,star_gift,gift_msg_id}', '') !~ '^[0-9]+$'
              OR (box.media #>> '{service_action,star_gift,gift_msg_id}')::numeric <= 0
              OR (box.media #>> '{service_action,star_gift,gift_msg_id}')::numeric > 2147483647
          )
    ) THEN
        RAISE EXCEPTION 'separate prepaid star gift message has malformed gift_msg_id';
    END IF;
END
$$;

CREATE TEMP TABLE star_gift_prepaid_message_aliases ON COMMIT DROP AS
SELECT DISTINCT owner_box.owner_user_id,
       owner_box.box_id,
       gift.id AS saved_gift_id,
       owner_box.message_sender_id,
       owner_box.private_message_id
FROM public.message_boxes owner_box
JOIN public.peer_star_gifts gift
  ON gift.owner_peer_type = 'user'
 AND gift.owner_peer_id = owner_box.owner_user_id
 AND gift.lifecycle_status = 'active'
 AND gift.msg_id = (owner_box.media #>> '{service_action,star_gift,gift_msg_id}')::integer
 AND gift.gift_id = (owner_box.media #>> '{service_action,star_gift,gift_id}')::bigint
WHERE NOT owner_box.deleted
  AND owner_box.media #>> '{service_action,kind}' = 'star_gift'
  AND owner_box.media #>> '{service_action,star_gift,prepaid_upgrade}' = 'true'
  AND owner_box.media #>> '{service_action,star_gift,upgrade_separate}' = 'true'
  AND owner_box.media #> '{service_action,star_gift,gift_msg_id}' IS NOT NULL;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM star_gift_prepaid_message_aliases
        GROUP BY owner_user_id, box_id
        HAVING COUNT(DISTINCT saved_gift_id) <> 1
    ) THEN
        RAISE EXCEPTION 'separate prepaid star gift message resolves to multiple aggregates';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM star_gift_prepaid_message_aliases alias
        JOIN public.star_gift_user_message_refs ref
          ON ref.owner_user_id = alias.owner_user_id
         AND ref.msg_id = alias.box_id
        WHERE ref.saved_gift_id <> alias.saved_gift_id
    ) THEN
        RAISE EXCEPTION 'separate prepaid star gift message collides with another aggregate';
    END IF;

    -- Both boxes of the logical private message must retain the same prepayment
    -- identity. The receiver-only gift_msg_id may differ by design.
    IF EXISTS (
        SELECT 1
        FROM star_gift_prepaid_message_aliases alias
        JOIN public.peer_star_gifts gift ON gift.id = alias.saved_gift_id
        JOIN public.message_boxes visible_box
          ON visible_box.message_sender_id = alias.message_sender_id
         AND visible_box.private_message_id = alias.private_message_id
         AND NOT visible_box.deleted
        WHERE visible_box.media #>> '{service_action,kind}' IS DISTINCT FROM 'star_gift'
           OR visible_box.media #>> '{service_action,star_gift,prepaid_upgrade}' IS DISTINCT FROM 'true'
           OR visible_box.media #>> '{service_action,star_gift,upgrade_separate}' IS DISTINCT FROM 'true'
           OR visible_box.media #>> '{service_action,star_gift,gift_id}' IS DISTINCT FROM gift.gift_id::text
    ) THEN
        RAISE EXCEPTION 'separate prepaid star gift private projections disagree';
    END IF;
END
$$;

CREATE UNIQUE INDEX star_gift_prepaid_message_aliases_owner_msg_idx
    ON star_gift_prepaid_message_aliases(owner_user_id, box_id);

INSERT INTO public.star_gift_user_message_refs(owner_user_id, msg_id, saved_gift_id)
SELECT owner_user_id, box_id, saved_gift_id
FROM star_gift_prepaid_message_aliases
ON CONFLICT (owner_user_id, msg_id) DO UPDATE
SET saved_gift_id = EXCLUDED.saved_gift_id
WHERE star_gift_user_message_refs.saved_gift_id = EXCLUDED.saved_gift_id;

COMMENT ON TABLE public.star_gift_user_message_refs IS
    'Owner-local service-message aliases (unique outputs and separate prepaid-upgrade notifications) for one saved gift aggregate.';

CREATE TEMP TABLE star_gift_prepaid_message_repairs (
    owner_user_id bigint NOT NULL,
    box_id integer NOT NULL,
    peer_type text NOT NULL,
    peer_id bigint NOT NULL,
    message_sender_id bigint NOT NULL,
    private_message_id bigint NOT NULL,
    repaired_media jsonb NOT NULL,
    PRIMARY KEY (owner_user_id, box_id)
) ON COMMIT DROP;

-- Upgrade every visible copy of an already-consumed prepayment. A viewer gets
-- upgrade_msg_id only when that same viewer owns a box for the emitted unique
-- action. This covers the original sender while avoiding an owner-local link
-- on an unrelated third-party payer's card.
INSERT INTO star_gift_prepaid_message_repairs(
    owner_user_id, box_id, peer_type, peer_id,
    message_sender_id, private_message_id, repaired_media
)
SELECT visible_box.owner_user_id,
       visible_box.box_id,
       visible_box.peer_type,
       visible_box.peer_id,
       visible_box.message_sender_id,
       visible_box.private_message_id,
       CASE
           WHEN unique_box.box_id IS NULL THEN
               visible_box.media
                   #- '{service_action,star_gift,can_upgrade}'
                   #- '{service_action,star_gift,prepaid_upgrade_hash}'
                   #- '{service_action,star_gift,upgrade_msg_id}'
           ELSE jsonb_set(
               visible_box.media
                   #- '{service_action,star_gift,can_upgrade}'
                   #- '{service_action,star_gift,prepaid_upgrade_hash}',
               '{service_action,star_gift,upgrade_msg_id}',
               to_jsonb(unique_box.box_id::bigint),
               true
           )
       END
FROM star_gift_prepaid_message_aliases alias
JOIN public.peer_star_gifts gift
  ON gift.id = alias.saved_gift_id
 AND gift.lifecycle_status = 'active'
 AND gift.unique_gift_id IS NOT NULL
 AND gift.upgrade_msg_id > 0
JOIN public.message_boxes owner_unique_box
  ON owner_unique_box.owner_user_id = gift.owner_peer_id
 AND owner_unique_box.box_id = gift.upgrade_msg_id
 AND NOT owner_unique_box.deleted
 AND owner_unique_box.media #>> '{service_action,kind}' = 'star_gift_unique'
 AND owner_unique_box.media #>> '{service_action,star_gift_unique,gift,ID}' = gift.unique_gift_id::text
JOIN public.message_boxes visible_box
  ON visible_box.message_sender_id = alias.message_sender_id
 AND visible_box.private_message_id = alias.private_message_id
 AND NOT visible_box.deleted
LEFT JOIN public.message_boxes unique_box
  ON unique_box.owner_user_id = visible_box.owner_user_id
 AND unique_box.message_sender_id = owner_unique_box.message_sender_id
 AND unique_box.private_message_id = owner_unique_box.private_message_id
 AND NOT unique_box.deleted
 AND unique_box.media #>> '{service_action,kind}' = 'star_gift_unique'
 AND unique_box.media #>> '{service_action,star_gift_unique,gift,ID}' = gift.unique_gift_id::text;

DO $$
DECLARE
    repair_row record;
    next_pts integer;
    event_date integer := EXTRACT(EPOCH FROM clock_timestamp())::integer;
BEGIN
    IF EXISTS (
        SELECT 1
        FROM star_gift_prepaid_message_aliases alias
        JOIN public.peer_star_gifts gift
          ON gift.id = alias.saved_gift_id
         AND gift.lifecycle_status = 'active'
         AND gift.unique_gift_id IS NOT NULL
                WHERE NOT EXISTS (
                    SELECT 1
                    FROM star_gift_prepaid_message_repairs target_repair
                    WHERE target_repair.owner_user_id = alias.owner_user_id
                      AND target_repair.box_id = alias.box_id
        )
    ) THEN
        RAISE EXCEPTION 'upgraded star gift is missing its prepaid message repair';
    END IF;

    FOR repair_row IN
        SELECT owner_user_id, box_id, peer_type, peer_id, repaired_media
        FROM star_gift_prepaid_message_repairs
        ORDER BY owner_user_id, box_id
    LOOP
        INSERT INTO public.user_update_watermarks(user_id, contiguous_pts)
        VALUES(repair_row.owner_user_id, 0)
        ON CONFLICT(user_id) DO NOTHING;

        UPDATE public.user_update_watermarks
        SET contiguous_pts = contiguous_pts + 1,
            updated_at = now()
        WHERE user_id = repair_row.owner_user_id
        RETURNING contiguous_pts INTO next_pts;

        UPDATE public.message_boxes
        SET media = repair_row.repaired_media,
            pts = next_pts
        WHERE owner_user_id = repair_row.owner_user_id
          AND box_id = repair_row.box_id
          AND NOT deleted;

        INSERT INTO public.user_update_events(
            user_id, pts, pts_count, date, event_type,
            message_box_id, peer_type, peer_id
        ) VALUES (
            repair_row.owner_user_id, next_pts, 1, event_date, 'edit_message',
            repair_row.box_id, repair_row.peer_type, repair_row.peer_id
        );

        INSERT INTO public.dispatch_outbox(
            target_user_id, pts, event_type,
            exclude_auth_key_id, exclude_session_id
        ) VALUES(repair_row.owner_user_id, next_pts, 'edit_message', 0, 0);
    END LOOP;
END
$$;

-- private_messages is a shared logical envelope and cannot retain either
-- participant's box-local gift_msg_id or upgrade_msg_id.
WITH shared_repairs AS (
    SELECT DISTINCT ON (repair.message_sender_id, repair.private_message_id)
           repair.message_sender_id,
           repair.private_message_id,
           repair.repaired_media
               #- '{service_action,star_gift,saved_id}'
               #- '{service_action,star_gift,gift_msg_id}'
               #- '{service_action,star_gift,upgrade_msg_id}' AS shared_media
    FROM star_gift_prepaid_message_repairs repair
    ORDER BY repair.message_sender_id,
             repair.private_message_id,
             (repair.owner_user_id = repair.message_sender_id) DESC,
             repair.owner_user_id
)
UPDATE public.private_messages private_message
SET media = repair.shared_media
FROM shared_repairs repair
WHERE private_message.sender_user_id = repair.message_sender_id
  AND private_message.id = repair.private_message_id;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM star_gift_prepaid_message_aliases alias
        LEFT JOIN public.star_gift_user_message_refs ref
          ON ref.owner_user_id = alias.owner_user_id
         AND ref.msg_id = alias.box_id
         AND ref.saved_gift_id = alias.saved_gift_id
        WHERE ref.saved_gift_id IS NULL
    ) THEN
        RAISE EXCEPTION 'separate prepaid star gift alias repair did not converge';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM star_gift_prepaid_message_repairs repair
        JOIN public.message_boxes box
          ON box.owner_user_id = repair.owner_user_id
         AND box.box_id = repair.box_id
        WHERE box.media IS DISTINCT FROM repair.repaired_media
           OR box.media #> '{service_action,star_gift,can_upgrade}' IS NOT NULL
           OR box.media #> '{service_action,star_gift,prepaid_upgrade_hash}' IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'upgraded prepaid star gift projection repair did not converge';
    END IF;
END
$$;
