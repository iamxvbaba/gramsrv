-- Official clients may continue lifecycle actions from a freshly emitted
-- messageActionStarGiftUnique. Keep those user-local message ids as explicit
-- durable references to the same saved gift aggregate.
CREATE TABLE star_gift_user_message_refs (
    owner_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    msg_id integer NOT NULL,
    saved_gift_id bigint NOT NULL REFERENCES peer_star_gifts(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_user_id, msg_id),
    CONSTRAINT star_gift_user_message_refs_msg_check CHECK (owner_user_id > 0 AND msg_id > 0)
);

CREATE INDEX star_gift_user_message_refs_saved_idx
    ON star_gift_user_message_refs(saved_gift_id, owner_user_id, msg_id);

INSERT INTO star_gift_user_message_refs(owner_user_id,msg_id,saved_gift_id)
SELECT box.owner_user_id, box.box_id, gift.id
FROM message_boxes box
JOIN unique_star_gifts unique_gift
  ON (box.media #>> '{service_action,star_gift_unique,gift,ID}') ~ '^[0-9]+$'
 AND unique_gift.id = (box.media #>> '{service_action,star_gift_unique,gift,ID}')::bigint
JOIN peer_star_gifts gift
  ON gift.id = unique_gift.source_saved_gift_id
 AND gift.owner_peer_type = 'user'
 AND gift.owner_peer_id = box.owner_user_id
WHERE NOT box.deleted
  AND box.media #>> '{service_action,kind}' = 'star_gift_unique'
  AND box.box_id <> gift.msg_id;

-- Hidden gifts cannot remain pinned. Compacting the whole owner vector here
-- also repairs historical gaps before the invariant is constrained.
ALTER TABLE peer_star_gifts
    ADD CONSTRAINT peer_star_gifts_hidden_unpinned_check
    CHECK (pinned_order<=6 AND (NOT unsaved OR pinned_order=0)) NOT VALID;

CREATE TEMP TABLE star_gift_pin_repairs ON COMMIT DROP AS
SELECT id,new_order
FROM (
    SELECT id,
           row_number() OVER (PARTITION BY owner_peer_type,owner_peer_id
                              ORDER BY pinned_order,id)::integer AS new_order
    FROM peer_star_gifts
    WHERE lifecycle_status='active' AND NOT unsaved AND pinned_order>0
) ranked
WHERE new_order<=6;

UPDATE peer_star_gifts SET pinned_order=0 WHERE pinned_order<>0;

UPDATE peer_star_gifts gift
SET pinned_order=repair.new_order
FROM star_gift_pin_repairs repair
WHERE gift.id=repair.id;

-- peer and saved_id share one TL flag and are channel-only. Earlier user gift
-- projections set peer=user (and sometimes a user box id in saved_id), which
-- made TDesktop select zero/stale ids instead of the emitted service message.
CREATE TEMP TABLE star_gift_user_unique_media_repairs (
    owner_user_id bigint NOT NULL,
    box_id integer NOT NULL,
    peer_type text NOT NULL,
    peer_id bigint NOT NULL,
    repaired_media jsonb NOT NULL,
    PRIMARY KEY(owner_user_id,box_id)
) ON COMMIT DROP;

INSERT INTO star_gift_user_unique_media_repairs(owner_user_id,box_id,peer_type,peer_id,repaired_media)
SELECT box.owner_user_id,
       box.box_id,
       box.peer_type,
       box.peer_id,
       jsonb_set(
           box.media #- '{service_action,star_gift_unique,saved_id}',
           '{service_action,star_gift_unique,peer}',
           '{"ID":0,"Type":""}'::jsonb,
           true
       )
FROM message_boxes box
WHERE NOT box.deleted
  AND box.media #>> '{service_action,kind}' = 'star_gift_unique'
  AND box.media #>> '{service_action,star_gift_unique,peer,Type}' = 'user';

DO $$
DECLARE
    repair record;
    next_pts integer;
    event_date integer := EXTRACT(EPOCH FROM clock_timestamp())::integer;
BEGIN
    FOR repair IN
        SELECT owner_user_id,box_id,peer_type,peer_id,repaired_media
        FROM star_gift_user_unique_media_repairs
        ORDER BY owner_user_id,box_id
    LOOP
        INSERT INTO user_update_watermarks(user_id,contiguous_pts)
        VALUES(repair.owner_user_id,0)
        ON CONFLICT(user_id) DO NOTHING;

        UPDATE user_update_watermarks
        SET contiguous_pts=contiguous_pts+1,updated_at=now()
        WHERE user_id=repair.owner_user_id
        RETURNING contiguous_pts INTO next_pts;

        UPDATE message_boxes
        SET media=repair.repaired_media,pts=next_pts
        WHERE owner_user_id=repair.owner_user_id AND box_id=repair.box_id AND NOT deleted;

        INSERT INTO user_update_events(
            user_id,pts,pts_count,date,event_type,message_box_id,peer_type,peer_id
        ) VALUES(
            repair.owner_user_id,next_pts,1,event_date,'edit_message',repair.box_id,repair.peer_type,repair.peer_id
        );

        INSERT INTO dispatch_outbox(
            target_user_id,pts,event_type,exclude_auth_key_id,exclude_session_id
        ) VALUES(repair.owner_user_id,next_pts,'edit_message',0,0);
    END LOOP;
END
$$;

UPDATE private_messages
SET media=jsonb_set(
    media #- '{service_action,star_gift_unique,saved_id}',
    '{service_action,star_gift_unique,peer}',
    '{"ID":0,"Type":""}'::jsonb,
    true
)
WHERE media #>> '{service_action,kind}' = 'star_gift_unique'
  AND media #>> '{service_action,star_gift_unique,peer,Type}' = 'user';
