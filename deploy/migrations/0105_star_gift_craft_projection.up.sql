-- Craft outcomes consume their inputs permanently. Keep the aggregate, every
-- visible messageActionStarGiftUnique snapshot, pts/outbox delivery and command
-- replay receipt in one state model.
ALTER TABLE public.star_gift_craft_commands
    ADD COLUMN source_edit_pts integer[] DEFAULT ARRAY[]::integer[] NOT NULL;

DO $$
DECLARE
    gift_box record;
    next_pts integer;
    event_date integer := EXTRACT(EPOCH FROM clock_timestamp())::integer;
    repaired_media jsonb;
BEGIN
    -- A successful legacy command would require reconstructing the crafted
    -- model snapshot, not merely flipping flags. Fail fast instead of silently
    -- fabricating that projection; the development database must be rebuilt or
    -- repaired explicitly if such a row ever exists.
    IF EXISTS (SELECT 1 FROM public.star_gift_craft_commands WHERE success) THEN
        RAISE EXCEPTION 'cannot migrate legacy successful craft command without an exact crafted message projection';
    END IF;

    -- upgrade_msg_id means the current owner's unique service-message
    -- projection, not permanently the first upgrade message. Ownership moves
    -- replace msg_id with the new transfer/resale/offer message; repair rows
    -- written before that invariant was enforced.
    UPDATE public.peer_star_gifts saved_gift
    SET upgrade_msg_id = saved_gift.msg_id
    WHERE saved_gift.owner_peer_type = 'user'
      AND saved_gift.unique_gift_id IS NOT NULL
      AND saved_gift.msg_id > 0
      AND EXISTS (
          SELECT 1
          FROM public.message_boxes box
          WHERE box.owner_user_id = saved_gift.owner_peer_id
            AND box.box_id = saved_gift.msg_id
            AND NOT box.deleted
            AND box.media #>> '{service_action,kind}' = 'star_gift_unique'
            AND (box.media #>> '{service_action,star_gift_unique,gift,ID}')::bigint = saved_gift.unique_gift_id
      );

    UPDATE public.peer_star_gifts
    SET upgrade_msg_id = 0
    WHERE owner_peer_type = 'channel'
      AND unique_gift_id IS NOT NULL
      AND upgrade_msg_id <> 0;

    UPDATE public.unique_star_gifts
    SET burned = true,
        craft_chance_permille = 0,
        offer_min_stars = 0,
        updated_at = now()
    WHERE burned;

    UPDATE public.peer_star_gifts
    SET lifecycle_status = 'burned',
        unsaved = true,
        pinned_order = 0,
        transfer_stars = 0,
        can_export_at = 0,
        can_transfer_at = 0,
        can_resell_at = 0,
        drop_original_details_stars = 0,
        can_craft_at = 0
    WHERE lifecycle_status = 'burned';

    FOR gift_box IN
        SELECT box.owner_user_id,
               box.box_id,
               box.peer_type,
               box.peer_id,
               box.message_sender_id,
               box.private_message_id,
               box.media
        FROM public.message_boxes box
        JOIN public.unique_star_gifts unique_gift
          ON unique_gift.id = (box.media #>> '{service_action,star_gift_unique,gift,ID}')::bigint
        WHERE box.media #>> '{service_action,kind}' = 'star_gift_unique'
          AND unique_gift.burned
          AND (
              COALESCE((box.media #>> '{service_action,star_gift_unique,gift,Burned}')::boolean, false) = false
              OR COALESCE((box.media #>> '{service_action,star_gift_unique,gift,CraftChancePermille}')::integer, 0) <> 0
              OR box.media #> '{service_action,star_gift_unique,saved}' IS NOT NULL
              OR box.media #> '{service_action,star_gift_unique,can_craft_at}' IS NOT NULL
          )
        ORDER BY box.owner_user_id, box.box_id
    LOOP
        repaired_media := jsonb_set(
                jsonb_set(
                    gift_box.media
                        #- '{service_action,star_gift_unique,gift,CraftChancePermille}'
                        #- '{service_action,star_gift_unique,saved}'
                        #- '{service_action,star_gift_unique,can_export_at}'
                        #- '{service_action,star_gift_unique,transfer_stars}'
                        #- '{service_action,star_gift_unique,resale_amount}'
                        #- '{service_action,star_gift_unique,can_transfer_at}'
                        #- '{service_action,star_gift_unique,can_resell_at}'
                        #- '{service_action,star_gift_unique,drop_original_details_stars}'
                        #- '{service_action,star_gift_unique,can_craft_at}',
                    '{service_action,star_gift_unique,gift,Burned}', 'true'::jsonb, true),
                '{service_action,star_gift_unique,gift,OfferMinStars}', '0'::jsonb, true);

        INSERT INTO public.user_update_watermarks (user_id, contiguous_pts)
        VALUES (gift_box.owner_user_id, 0)
        ON CONFLICT (user_id) DO NOTHING;

        UPDATE public.user_update_watermarks
        SET contiguous_pts = contiguous_pts + 1,
            updated_at = now()
        WHERE user_id = gift_box.owner_user_id
        RETURNING contiguous_pts INTO next_pts;

        UPDATE public.message_boxes
        SET media = repaired_media,
            pts = next_pts
        WHERE owner_user_id = gift_box.owner_user_id
          AND box_id = gift_box.box_id
          AND NOT deleted;

        UPDATE public.private_messages
        SET media = jsonb_set(
                jsonb_set(
                    media
                        #- '{service_action,star_gift_unique,gift,CraftChancePermille}'
                        #- '{service_action,star_gift_unique,saved}'
                        #- '{service_action,star_gift_unique,can_export_at}'
                        #- '{service_action,star_gift_unique,transfer_stars}'
                        #- '{service_action,star_gift_unique,resale_amount}'
                        #- '{service_action,star_gift_unique,can_transfer_at}'
                        #- '{service_action,star_gift_unique,can_resell_at}'
                        #- '{service_action,star_gift_unique,drop_original_details_stars}'
                        #- '{service_action,star_gift_unique,can_craft_at}',
                    '{service_action,star_gift_unique,gift,Burned}', 'true'::jsonb, true),
                '{service_action,star_gift_unique,gift,OfferMinStars}', '0'::jsonb, true),
            sender_snapshot = jsonb_set(
                jsonb_set(
                    sender_snapshot
                        #- '{message,Media,service_action,star_gift_unique,gift,CraftChancePermille}'
                        #- '{message,Media,service_action,star_gift_unique,saved}'
                        #- '{message,Media,service_action,star_gift_unique,can_export_at}'
                        #- '{message,Media,service_action,star_gift_unique,transfer_stars}'
                        #- '{message,Media,service_action,star_gift_unique,resale_amount}'
                        #- '{message,Media,service_action,star_gift_unique,can_transfer_at}'
                        #- '{message,Media,service_action,star_gift_unique,can_resell_at}'
                        #- '{message,Media,service_action,star_gift_unique,drop_original_details_stars}'
                        #- '{message,Media,service_action,star_gift_unique,can_craft_at}',
                    '{message,Media,service_action,star_gift_unique,gift,Burned}', 'true'::jsonb, true),
                '{message,Media,service_action,star_gift_unique,gift,OfferMinStars}', '0'::jsonb, true)
        WHERE sender_user_id = gift_box.message_sender_id
          AND id = gift_box.private_message_id;

        INSERT INTO public.user_update_events (
            user_id, pts, pts_count, date, event_type,
            message_box_id, peer_type, peer_id
        ) VALUES (
            gift_box.owner_user_id, next_pts, 1, event_date, 'edit_message',
            gift_box.box_id, gift_box.peer_type, gift_box.peer_id
        );

        INSERT INTO public.dispatch_outbox (
            target_user_id, pts, event_type,
            exclude_auth_key_id, exclude_session_id
        ) VALUES (
            gift_box.owner_user_id, next_pts, 'edit_message', 0, 0
        );
    END LOOP;

    UPDATE public.star_gift_craft_commands command
    SET source_edit_pts = (
        SELECT array_agg(box.pts ORDER BY input.ordinality)::integer[] AS pts
        FROM unnest(command.input_unique_gift_ids) WITH ORDINALITY AS input(unique_gift_id, ordinality)
        JOIN public.unique_star_gifts unique_gift ON unique_gift.id = input.unique_gift_id
        JOIN public.peer_star_gifts saved_gift ON saved_gift.id = unique_gift.source_saved_gift_id
        JOIN public.message_boxes box
          ON box.owner_user_id = command.user_id
         AND box.box_id = saved_gift.upgrade_msg_id
         AND NOT box.deleted
    )
    WHERE NOT command.success;

    IF EXISTS (
        SELECT 1
        FROM public.star_gift_craft_commands
        WHERE cardinality(source_edit_pts) <> cardinality(input_unique_gift_ids)
           OR array_position(source_edit_pts, 0) IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'craft command is missing an exact source message edit receipt';
    END IF;
END
$$;

ALTER TABLE public.star_gift_craft_commands
    ADD CONSTRAINT star_gift_craft_source_edit_shape_check CHECK (
        cardinality(source_edit_pts) = cardinality(input_unique_gift_ids)
        AND array_position(source_edit_pts, 0) IS NULL
    );
