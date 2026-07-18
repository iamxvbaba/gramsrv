-- Migration 0100 corrected the durable Star Gift service-message JSON, but a
-- TDesktop that had already cached the old message would otherwise keep using
-- the conflated outer upgrade_stars field. Publish one durable edit event for
-- every private message box whose paid-upgrade price was split by 0100.
--
-- This is deliberately a pts event, not a cache-only notification: history,
-- updates.getDifference and online outbox delivery must all expose the same
-- corrected message snapshot. The migration transaction keeps the message
-- pts, user watermark, durable event and dispatch task atomic.
DO $$
DECLARE
    gift_box record;
    next_pts integer;
    event_date integer := EXTRACT(EPOCH FROM clock_timestamp())::integer;
BEGIN
    FOR gift_box IN
        SELECT owner_user_id, box_id, peer_type, peer_id
        FROM public.message_boxes
        WHERE media #>> '{service_action,kind}' = 'star_gift'
          AND jsonb_typeof(media #> '{service_action,star_gift,upgrade_price_stars}') = 'number'
          AND (media #>> '{service_action,star_gift,upgrade_price_stars}')::bigint > 0
        ORDER BY owner_user_id, box_id
    LOOP
        INSERT INTO public.user_update_watermarks (user_id, contiguous_pts)
        VALUES (gift_box.owner_user_id, 0)
        ON CONFLICT (user_id) DO NOTHING;

        UPDATE public.user_update_watermarks
        SET contiguous_pts = contiguous_pts + 1,
            updated_at = now()
        WHERE user_id = gift_box.owner_user_id
        RETURNING contiguous_pts INTO next_pts;

        UPDATE public.message_boxes
        SET pts = next_pts
        WHERE owner_user_id = gift_box.owner_user_id
          AND box_id = gift_box.box_id;

        INSERT INTO public.user_update_events (
            user_id,
            pts,
            pts_count,
            date,
            event_type,
            message_box_id,
            peer_type,
            peer_id
        ) VALUES (
            gift_box.owner_user_id,
            next_pts,
            1,
            event_date,
            'edit_message',
            gift_box.box_id,
            gift_box.peer_type,
            gift_box.peer_id
        );

        INSERT INTO public.dispatch_outbox (
            target_user_id,
            pts,
            event_type,
            exclude_auth_key_id,
            exclude_session_id
        ) VALUES (
            gift_box.owner_user_id,
            next_pts,
            'edit_message',
            0,
            0
        );
    END LOOP;
END
$$;
