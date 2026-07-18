-- Craft is a protocol capability, not a generic property of every collectible.
-- A non-zero craft_chance_permille is valid only when the immutable official
-- collectible revision contains at least one crafted model. Earlier versions
-- advertised the default chance for every upgrade, including official sets
-- such as Fresh Socks whose snapshot has no crafted model at all.
--
-- Repair the aggregate and every durable private-message projection together.
-- Each visible message edit advances pts and is recoverable through both live
-- outbox delivery and updates.getDifference.
DO $$
DECLARE
    gift_box record;
    next_pts integer;
    event_date integer := EXTRACT(EPOCH FROM clock_timestamp())::integer;
    repaired_media jsonb;
BEGIN
    UPDATE public.unique_star_gifts unique_gift
    SET craft_chance_permille = 0,
        updated_at = now()
    WHERE unique_gift.craft_chance_permille > 0
      AND NOT EXISTS (
          SELECT 1
          FROM public.star_gift_collectible_models model
          WHERE model.collectible_revision_id = unique_gift.collectible_revision_id
            AND model.crafted
      );

    UPDATE public.peer_star_gifts saved_gift
    SET can_craft_at = 0
    FROM public.unique_star_gifts unique_gift
    WHERE unique_gift.id = saved_gift.unique_gift_id
      AND unique_gift.craft_chance_permille = 0
      AND NOT EXISTS (
          SELECT 1
          FROM public.star_gift_collectible_models model
          WHERE model.collectible_revision_id = unique_gift.collectible_revision_id
            AND model.crafted
      );

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
          AND jsonb_typeof(box.media #> '{service_action,star_gift_unique,gift,CraftChancePermille}') = 'number'
          AND (box.media #>> '{service_action,star_gift_unique,gift,CraftChancePermille}')::integer > 0
          AND NOT EXISTS (
              SELECT 1
              FROM public.star_gift_collectible_models model
              WHERE model.collectible_revision_id = unique_gift.collectible_revision_id
                AND model.crafted
          )
        ORDER BY box.owner_user_id, box.box_id
    LOOP
        repaired_media := gift_box.media
            #- '{service_action,star_gift_unique,gift,CraftChancePermille}'
            #- '{service_action,star_gift_unique,can_craft_at}';

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
        SET media = media
                #- '{service_action,star_gift_unique,gift,CraftChancePermille}'
                #- '{service_action,star_gift_unique,can_craft_at}',
            sender_snapshot = sender_snapshot
                #- '{message,Media,service_action,star_gift_unique,gift,CraftChancePermille}'
                #- '{message,Media,service_action,star_gift_unique,can_craft_at}'
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
END
$$;
