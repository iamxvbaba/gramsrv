-- Official Android clients use a positive can_craft_at both as the Craft
-- capability marker and as the readiness boundary. Earlier zero-delay
-- upgrades persisted 0 while retaining a positive craft chance, so TDesktop
-- could Craft the gift but Android hid the entry entirely.
--
-- Block concurrent lifecycle/message writers while aggregate facts, message
-- snapshots and durable edit edges are repaired in this migration transaction.
LOCK TABLE public.peer_star_gifts, public.unique_star_gifts,
    public.message_boxes, public.private_messages IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
    -- Craft capability remains an intrinsic collectible fact while ownership
    -- moves between users and channels. Terminal/external states cannot Craft.
    UPDATE public.unique_star_gifts unique_gift
    SET craft_chance_permille = 0,
        updated_at = now()
    FROM public.peer_star_gifts saved_gift
    WHERE saved_gift.unique_gift_id = unique_gift.id
      AND unique_gift.craft_chance_permille > 0
      AND (
          saved_gift.lifecycle_status <> 'active'
          OR unique_gift.owner_address <> ''
          OR unique_gift.burned
          OR unique_gift.crafted
      );

    UPDATE public.peer_star_gifts saved_gift
    SET can_craft_at = 0
    FROM public.unique_star_gifts unique_gift
    WHERE unique_gift.id = saved_gift.unique_gift_id
      AND unique_gift.craft_chance_permille = 0
      AND saved_gift.can_craft_at <> 0;

    IF EXISTS (
        SELECT 1
        FROM public.unique_star_gifts unique_gift
        JOIN public.peer_star_gifts saved_gift
          ON saved_gift.unique_gift_id = unique_gift.id
        WHERE unique_gift.craft_chance_permille > 0
          AND (
              saved_gift.owner_peer_type NOT IN ('user', 'channel')
              OR saved_gift.lifecycle_status <> 'active'
              OR unique_gift.owner_address <> ''
              OR unique_gift.burned
              OR unique_gift.crafted
              OR NOT EXISTS (
                  SELECT 1
                  FROM public.star_gift_collectible_models model
                  WHERE model.collectible_revision_id = unique_gift.collectible_revision_id
                    AND model.crafted
              )
          )
    ) THEN
        RAISE EXCEPTION 'positive star gift craft chance has no valid owned aggregate';
    END IF;

    -- created_at is the stable persisted proxy for the original upgrade
    -- transaction date on legacy rows. New writes use the exact request date.
    UPDATE public.peer_star_gifts saved_gift
    SET can_craft_at = GREATEST(
        1,
        LEAST(2147483647, FLOOR(EXTRACT(EPOCH FROM unique_gift.created_at))::bigint)::integer
    )
    FROM public.unique_star_gifts unique_gift
    WHERE unique_gift.id = saved_gift.unique_gift_id
      AND unique_gift.craft_chance_permille > 0
      AND saved_gift.can_craft_at = 0;

    IF EXISTS (
        SELECT 1
        FROM public.peer_star_gifts saved_gift
        JOIN public.unique_star_gifts unique_gift
          ON unique_gift.id = saved_gift.unique_gift_id
        WHERE (unique_gift.craft_chance_permille > 0)
              IS DISTINCT FROM (saved_gift.can_craft_at > 0)
    ) THEN
        RAISE EXCEPTION 'star gift craft chance/readiness repair did not converge';
    END IF;
END
$$;

CREATE TEMP TABLE star_gift_craft_message_repairs (
    owner_user_id bigint NOT NULL,
    box_id integer NOT NULL,
    unique_gift_id bigint NOT NULL,
    desired_craft_chance integer NOT NULL,
    desired_can_craft_at integer NOT NULL,
    PRIMARY KEY (owner_user_id, box_id)
) ON COMMIT DROP;

-- Adding capability is owner-scoped: repair only the current owner's
-- authoritative unique action (upgrade_msg_id) and the other visible box of
-- that same logical private message. Never add Craft back to an old owner's
-- historical transfer/resale action.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.peer_star_gifts saved_gift
        JOIN public.unique_star_gifts unique_gift
          ON unique_gift.id = saved_gift.unique_gift_id
        WHERE saved_gift.owner_peer_type = 'user'
          AND saved_gift.lifecycle_status = 'active'
          AND saved_gift.can_craft_at > 0
          AND NOT EXISTS (
              SELECT 1
              FROM public.message_boxes owner_box
              WHERE owner_box.owner_user_id = saved_gift.owner_peer_id
                AND owner_box.box_id = saved_gift.upgrade_msg_id
                AND NOT owner_box.deleted
                AND owner_box.media #>> '{service_action,kind}' = 'star_gift_unique'
                AND owner_box.media #>> '{service_action,star_gift_unique,gift,ID}' = unique_gift.id::text
          )
    ) THEN
        RAISE EXCEPTION 'craftable star gift is missing its current owner action';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM public.message_boxes box
        WHERE box.media #>> '{service_action,kind}' = 'star_gift_unique'
          AND box.media #> '{service_action,star_gift_unique,can_craft_at}' IS NOT NULL
          AND (
              jsonb_typeof(box.media #> '{service_action,star_gift_unique,can_craft_at}') <> 'number'
              OR COALESCE(box.media #>> '{service_action,star_gift_unique,can_craft_at}', '') !~ '^[0-9]+$'
              OR (box.media #>> '{service_action,star_gift_unique,can_craft_at}')::numeric > 2147483647
          )
    ) THEN
        RAISE EXCEPTION 'star gift message has malformed can_craft_at';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM public.message_boxes box
        WHERE box.media #>> '{service_action,kind}' = 'star_gift_unique'
          AND box.media #> '{service_action,star_gift_unique,gift,CraftChancePermille}' IS NOT NULL
          AND (
              jsonb_typeof(box.media #> '{service_action,star_gift_unique,gift,CraftChancePermille}') <> 'number'
              OR COALESCE(box.media #>> '{service_action,star_gift_unique,gift,CraftChancePermille}', '') !~ '^[0-9]+$'
              OR (box.media #>> '{service_action,star_gift_unique,gift,CraftChancePermille}')::numeric > 1000
          )
    ) THEN
        RAISE EXCEPTION 'star gift message has malformed craft chance';
    END IF;
END
$$;

INSERT INTO star_gift_craft_message_repairs(
    owner_user_id, box_id, unique_gift_id,
    desired_craft_chance, desired_can_craft_at
)
SELECT visible_box.owner_user_id,
       visible_box.box_id,
       unique_gift.id,
       unique_gift.craft_chance_permille,
       saved_gift.can_craft_at
FROM public.peer_star_gifts saved_gift
JOIN public.unique_star_gifts unique_gift
  ON unique_gift.id = saved_gift.unique_gift_id
JOIN public.message_boxes owner_box
  ON owner_box.owner_user_id = saved_gift.owner_peer_id
 AND owner_box.box_id = saved_gift.upgrade_msg_id
 AND NOT owner_box.deleted
 AND owner_box.media #>> '{service_action,star_gift_unique,gift,ID}' = unique_gift.id::text
JOIN public.message_boxes visible_box
  ON visible_box.message_sender_id = owner_box.message_sender_id
 AND visible_box.private_message_id = owner_box.private_message_id
 AND NOT visible_box.deleted
WHERE saved_gift.owner_peer_type = 'user'
  AND saved_gift.lifecycle_status = 'active'
  AND saved_gift.can_craft_at > 0
  AND (
      COALESCE(NULLIF(visible_box.media #>> '{service_action,star_gift_unique,can_craft_at}', '')::integer, 0)
          IS DISTINCT FROM saved_gift.can_craft_at
      OR COALESCE(NULLIF(visible_box.media #>> '{service_action,star_gift_unique,gift,CraftChancePermille}', '')::integer, 0)
          IS DISTINCT FROM unique_gift.craft_chance_permille
  );

-- Only the current owner's authoritative logical message may expose Craft.
-- Terminal gifts, channel-owned gifts (until channel Craft is implemented),
-- and old-owner historical actions must have both wire markers removed.
INSERT INTO star_gift_craft_message_repairs(
    owner_user_id, box_id, unique_gift_id,
    desired_craft_chance, desired_can_craft_at
)
SELECT box.owner_user_id, box.box_id, unique_gift.id, 0, 0
FROM public.message_boxes box
JOIN public.unique_star_gifts unique_gift
  ON (box.media #>> '{service_action,star_gift_unique,gift,ID}') ~ '^[0-9]+$'
 AND unique_gift.id = (box.media #>> '{service_action,star_gift_unique,gift,ID}')::bigint
JOIN public.peer_star_gifts saved_gift
  ON saved_gift.unique_gift_id = unique_gift.id
WHERE NOT box.deleted
  AND box.media #>> '{service_action,kind}' = 'star_gift_unique'
  AND (
      box.media #> '{service_action,star_gift_unique,can_craft_at}' IS NOT NULL
      OR box.media #> '{service_action,star_gift_unique,gift,CraftChancePermille}' IS NOT NULL
  )
  AND NOT EXISTS (
      SELECT 1
      FROM public.message_boxes authority
      WHERE saved_gift.owner_peer_type = 'user'
        AND saved_gift.lifecycle_status = 'active'
        AND saved_gift.can_craft_at > 0
        AND unique_gift.craft_chance_permille > 0
        AND authority.owner_user_id = saved_gift.owner_peer_id
        AND authority.box_id = saved_gift.upgrade_msg_id
        AND NOT authority.deleted
        AND authority.media #>> '{service_action,kind}' = 'star_gift_unique'
        AND authority.media #>> '{service_action,star_gift_unique,gift,ID}' = unique_gift.id::text
        AND authority.message_sender_id = box.message_sender_id
        AND authority.private_message_id = box.private_message_id
  )
ON CONFLICT (owner_user_id, box_id) DO UPDATE
SET unique_gift_id = EXCLUDED.unique_gift_id,
    desired_craft_chance = 0,
    desired_can_craft_at = 0;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM star_gift_craft_message_repairs target
        JOIN public.message_boxes box
          ON box.owner_user_id = target.owner_user_id
         AND box.box_id = target.box_id
        WHERE box.deleted
           OR box.media #>> '{service_action,kind}' <> 'star_gift_unique'
           OR box.media #>> '{service_action,star_gift_unique,gift,ID}' <> target.unique_gift_id::text
    ) THEN
        RAISE EXCEPTION 'craft readiness repair target is not the expected unique gift action';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM star_gift_craft_message_repairs target
        JOIN public.message_boxes box
          ON box.owner_user_id = target.owner_user_id
         AND box.box_id = target.box_id
        WHERE NOT EXISTS (
            SELECT 1
            FROM public.private_messages private_message
            WHERE private_message.sender_user_id = box.message_sender_id
              AND private_message.id = box.private_message_id
              AND private_message.media #>> '{service_action,kind}' = 'star_gift_unique'
              AND private_message.media #>> '{service_action,star_gift_unique,gift,ID}' = target.unique_gift_id::text
        )
    ) THEN
        RAISE EXCEPTION 'craft readiness repair target has no matching private message';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM star_gift_craft_message_repairs target
        JOIN public.message_boxes box
          ON box.owner_user_id = target.owner_user_id
         AND box.box_id = target.box_id
        GROUP BY box.message_sender_id, box.private_message_id
        HAVING COUNT(DISTINCT (
            target.unique_gift_id,
            target.desired_craft_chance,
            target.desired_can_craft_at
        )) <> 1
    ) THEN
        RAISE EXCEPTION 'craft readiness repair has conflicting logical message targets';
    END IF;
END
$$;

DO $$
DECLARE
    repair record;
    next_pts integer;
    event_date integer := LEAST(2147483647, EXTRACT(EPOCH FROM clock_timestamp())::bigint)::integer;
    repaired_media jsonb;
    repaired_private_media jsonb;
    affected_rows bigint;
BEGIN
    FOR repair IN
        SELECT target.owner_user_id,
               target.box_id,
               target.unique_gift_id,
               target.desired_craft_chance,
               target.desired_can_craft_at,
               box.peer_type,
               box.peer_id,
               box.message_sender_id,
               box.private_message_id,
               box.media
        FROM star_gift_craft_message_repairs target
        JOIN public.message_boxes box
          ON box.owner_user_id = target.owner_user_id
         AND box.box_id = target.box_id
         AND NOT box.deleted
        ORDER BY target.owner_user_id, target.box_id
        FOR UPDATE OF box
    LOOP
        IF repair.desired_craft_chance > 0 THEN
            repaired_media := jsonb_set(
                jsonb_set(
                    repair.media,
                    '{service_action,star_gift_unique,gift,CraftChancePermille}',
                    to_jsonb(repair.desired_craft_chance),
                    true
                ),
                '{service_action,star_gift_unique,can_craft_at}',
                to_jsonb(repair.desired_can_craft_at),
                true
            );
        ELSE
            repaired_media := repair.media
                #- '{service_action,star_gift_unique,can_craft_at}'
                #- '{service_action,star_gift_unique,gift,CraftChancePermille}';
        END IF;

        IF repaired_media #>> '{service_action,kind}' <> 'star_gift_unique'
           OR repaired_media #>> '{service_action,star_gift_unique,gift,ID}' <> repair.unique_gift_id::text
           OR (
               repair.desired_craft_chance > 0
               AND (
                   repaired_media #>> '{service_action,star_gift_unique,gift,CraftChancePermille}'
                       IS DISTINCT FROM repair.desired_craft_chance::text
                   OR repaired_media #>> '{service_action,star_gift_unique,can_craft_at}'
                       IS DISTINCT FROM repair.desired_can_craft_at::text
               )
           )
           OR (
               repair.desired_craft_chance = 0
               AND (
                   repaired_media #> '{service_action,star_gift_unique,gift,CraftChancePermille}' IS NOT NULL
                   OR repaired_media #> '{service_action,star_gift_unique,can_craft_at}' IS NOT NULL
               )
           ) THEN
            RAISE EXCEPTION 'craft readiness repair cannot project message box for user %, box %',
                repair.owner_user_id, repair.box_id;
        END IF;

        INSERT INTO public.user_update_watermarks (user_id, contiguous_pts)
        VALUES (repair.owner_user_id, 0)
        ON CONFLICT (user_id) DO NOTHING;

        UPDATE public.user_update_watermarks
        SET contiguous_pts = contiguous_pts + 1,
            updated_at = now()
        WHERE user_id = repair.owner_user_id
        RETURNING contiguous_pts INTO next_pts;

        UPDATE public.message_boxes
        SET media = repaired_media,
            pts = next_pts
        WHERE owner_user_id = repair.owner_user_id
          AND box_id = repair.box_id
          AND NOT deleted;
        GET DIAGNOSTICS affected_rows = ROW_COUNT;
        IF affected_rows <> 1 THEN
            RAISE EXCEPTION 'craft readiness repair lost user %, box %', repair.owner_user_id, repair.box_id;
        END IF;

        SELECT media
          INTO repaired_private_media
          FROM public.private_messages
         WHERE sender_user_id = repair.message_sender_id
           AND id = repair.private_message_id
         FOR UPDATE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'craft readiness repair missing private message for user %, box %',
                repair.owner_user_id, repair.box_id;
        END IF;

        IF repaired_private_media #>> '{service_action,kind}' <> 'star_gift_unique'
           OR repaired_private_media #>> '{service_action,star_gift_unique,gift,ID}' <> repair.unique_gift_id::text THEN
            RAISE EXCEPTION 'craft readiness repair found mismatched private message for user %, box %',
                repair.owner_user_id, repair.box_id;
        END IF;

        IF repair.desired_craft_chance > 0 THEN
            repaired_private_media := jsonb_set(
                jsonb_set(
                    repaired_private_media,
                    '{service_action,star_gift_unique,gift,CraftChancePermille}',
                    to_jsonb(repair.desired_craft_chance),
                    true
                ),
                '{service_action,star_gift_unique,can_craft_at}',
                to_jsonb(repair.desired_can_craft_at),
                true
            );
            IF repaired_private_media #>> '{service_action,star_gift_unique,can_craft_at}'
                    IS DISTINCT FROM repair.desired_can_craft_at::text
               OR repaired_private_media #>> '{service_action,star_gift_unique,gift,CraftChancePermille}'
                    IS DISTINCT FROM repair.desired_craft_chance::text THEN
                RAISE EXCEPTION 'craft readiness repair cannot project private message for user %, box %',
                    repair.owner_user_id, repair.box_id;
            END IF;
        ELSE
            repaired_private_media := repaired_private_media
                #- '{service_action,star_gift_unique,can_craft_at}'
                #- '{service_action,star_gift_unique,gift,CraftChancePermille}';
            IF repaired_private_media #> '{service_action,star_gift_unique,can_craft_at}' IS NOT NULL
               OR repaired_private_media #> '{service_action,star_gift_unique,gift,CraftChancePermille}' IS NOT NULL THEN
                RAISE EXCEPTION 'craft readiness repair cannot clear private message for user %, box %',
                    repair.owner_user_id, repair.box_id;
            END IF;
        END IF;

        UPDATE public.private_messages
        SET media = repaired_private_media
        WHERE sender_user_id = repair.message_sender_id
          AND id = repair.private_message_id;
        GET DIAGNOSTICS affected_rows = ROW_COUNT;
        IF affected_rows <> 1 THEN
            RAISE EXCEPTION 'craft readiness repair lost private message for user %, box %',
                repair.owner_user_id, repair.box_id;
        END IF;

        INSERT INTO public.user_update_events (
            user_id, pts, pts_count, date, event_type,
            message_box_id, peer_type, peer_id
        ) VALUES (
            repair.owner_user_id, next_pts, 1, event_date, 'edit_message',
            repair.box_id, repair.peer_type, repair.peer_id
        );

        INSERT INTO public.dispatch_outbox (
            target_user_id, pts, event_type,
            exclude_auth_key_id, exclude_session_id
        ) VALUES (
            repair.owner_user_id, next_pts, 'edit_message', 0, 0
        );
    END LOOP;
END
$$;

-- Extend the existing deferred unique/saved aggregate guard. Upgrade, Craft
-- and export update the two tables in separate statements, so commit-time
-- validation observes the final atomic state without a read fallback.
CREATE OR REPLACE FUNCTION public.telesrv_check_unique_star_gift_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    unique_id bigint;
    gift_owner_type text;
    gift_owner_id bigint;
    gift_owner_address text;
    gift_burned boolean;
    gift_crafted boolean;
    gift_craft_chance integer;
    gift_revision_id bigint;
    saved_status text;
    saved_owner_type text;
    saved_owner_id bigint;
    saved_can_craft_at integer;
BEGIN
    IF TG_TABLE_NAME = 'unique_star_gifts' THEN
        unique_id := COALESCE(NEW.id, OLD.id);
    ELSE
        unique_id := COALESCE(NEW.unique_gift_id, OLD.unique_gift_id);
    END IF;
    IF unique_id IS NULL THEN RETURN NULL; END IF;
    SELECT owner_peer_type, owner_peer_id, owner_address, burned, crafted,
           craft_chance_permille, collectible_revision_id
      INTO gift_owner_type, gift_owner_id, gift_owner_address, gift_burned, gift_crafted,
           gift_craft_chance, gift_revision_id
      FROM public.unique_star_gifts WHERE id=unique_id;
    IF NOT FOUND THEN RETURN NULL; END IF;
    SELECT lifecycle_status, owner_peer_type, owner_peer_id, can_craft_at
      INTO saved_status, saved_owner_type, saved_owner_id, saved_can_craft_at
      FROM public.peer_star_gifts WHERE unique_gift_id=unique_id;
    IF NOT FOUND THEN RAISE EXCEPTION 'unique star gift missing saved aggregate'; END IF;
    IF gift_burned THEN
        IF saved_status <> 'burned' THEN RAISE EXCEPTION 'burned unique star gift has live saved aggregate'; END IF;
    ELSIF gift_owner_address <> '' THEN
        IF saved_status <> 'exported' THEN RAISE EXCEPTION 'exported unique star gift has non-exported saved aggregate'; END IF;
    ELSIF saved_status <> 'active' OR gift_owner_type IS DISTINCT FROM saved_owner_type OR gift_owner_id IS DISTINCT FROM saved_owner_id THEN
        RAISE EXCEPTION 'unique star gift owner mismatch';
    END IF;
    IF gift_craft_chance > 0 THEN
        IF saved_can_craft_at <= 0
           OR saved_status <> 'active'
           OR saved_owner_type NOT IN ('user', 'channel')
           OR gift_owner_address <> ''
           OR gift_burned
           OR gift_crafted THEN
            RAISE EXCEPTION 'unique star gift craft capability has invalid aggregate state';
        END IF;
        IF NOT EXISTS (
            SELECT 1
            FROM public.star_gift_collectible_models model
            WHERE model.collectible_revision_id = gift_revision_id
              AND model.crafted
        ) THEN
            RAISE EXCEPTION 'unique star gift craft chance has no crafted model';
        END IF;
    ELSIF saved_can_craft_at <> 0 THEN
        RAISE EXCEPTION 'unique star gift readiness exists without craft chance';
    END IF;
    RETURN NULL;
END;
$$;
