-- A user-owned upgraded gift has one stable protocol identity: the original
-- gift service-message id. The unique service message points back to it via
-- saved_id, while the original message points forward to the box-local unique
-- service message via upgrade_msg_id. Both projections must be durable pts
-- edits so history, live delivery and updates.getDifference agree.

ALTER TABLE public.star_gift_upgrade_commands
    ADD COLUMN source_edit_pts integer DEFAULT 0 NOT NULL,
    ADD CONSTRAINT star_gift_upgrade_command_source_edit_pts_check CHECK (source_edit_pts >= 0);

DO $$
DECLARE
    gift record;
    source_root record;
    upgrade_root record;
    pair record;
    next_pts integer;
    event_date integer := EXTRACT(EPOCH FROM clock_timestamp())::integer;
    repaired_source_media jsonb;
    repaired_unique_media jsonb;
    private_media jsonb;
BEGIN
    FOR gift IN
        SELECT id, owner_peer_id, msg_id, upgrade_msg_id
        FROM public.peer_star_gifts
        WHERE owner_peer_type = 'user'
          AND unique_gift_id IS NOT NULL
          AND lifecycle_status = 'active'
          AND msg_id > 0
          AND upgrade_msg_id > 0
        ORDER BY id
    LOOP
        SELECT private_message_id, message_sender_id
        INTO STRICT source_root
        FROM public.message_boxes
        WHERE owner_user_id = gift.owner_peer_id
          AND box_id = gift.msg_id
          AND NOT deleted;

        SELECT private_message_id, message_sender_id
        INTO STRICT upgrade_root
        FROM public.message_boxes
        WHERE owner_user_id = gift.owner_peer_id
          AND box_id = gift.upgrade_msg_id
          AND NOT deleted;

        FOR pair IN
            SELECT source_box.owner_user_id,
                   source_box.box_id AS source_box_id,
                   source_box.peer_type AS source_peer_type,
                   source_box.peer_id AS source_peer_id,
                   source_box.media AS source_media,
                   unique_box.box_id AS unique_box_id,
                   unique_box.peer_type AS unique_peer_type,
                   unique_box.peer_id AS unique_peer_id,
                   unique_box.media AS unique_media
            FROM public.message_boxes source_box
            JOIN public.message_boxes unique_box
              ON unique_box.owner_user_id = source_box.owner_user_id
             AND unique_box.message_sender_id = upgrade_root.message_sender_id
             AND unique_box.private_message_id = upgrade_root.private_message_id
             AND NOT unique_box.deleted
            WHERE source_box.message_sender_id = source_root.message_sender_id
              AND source_box.private_message_id = source_root.private_message_id
              AND NOT source_box.deleted
            ORDER BY source_box.owner_user_id
        LOOP
            IF pair.source_media #>> '{service_action,kind}' <> 'star_gift' THEN
                RAISE EXCEPTION 'saved gift % source box % has invalid service action', gift.id, pair.source_box_id;
            END IF;
            IF pair.unique_media #>> '{service_action,kind}' <> 'star_gift_unique' THEN
                RAISE EXCEPTION 'saved gift % unique box % has invalid service action', gift.id, pair.unique_box_id;
            END IF;

            repaired_source_media := jsonb_set(
                pair.source_media,
                '{service_action,star_gift,upgrade_msg_id}',
                to_jsonb(pair.unique_box_id::bigint),
                true
            ) #- '{service_action,star_gift,can_upgrade}';
            repaired_unique_media := jsonb_set(
                pair.unique_media,
                '{service_action,star_gift_unique,saved_id}',
                to_jsonb(pair.source_box_id::bigint),
                true
            );

            IF pair.source_media IS DISTINCT FROM repaired_source_media THEN
                INSERT INTO public.user_update_watermarks (user_id, contiguous_pts)
                VALUES (pair.owner_user_id, 0)
                ON CONFLICT (user_id) DO NOTHING;

                UPDATE public.user_update_watermarks
                SET contiguous_pts = contiguous_pts + 1,
                    updated_at = now()
                WHERE user_id = pair.owner_user_id
                RETURNING contiguous_pts INTO next_pts;

                UPDATE public.message_boxes
                SET media = repaired_source_media,
                    pts = next_pts
                WHERE owner_user_id = pair.owner_user_id
                  AND box_id = pair.source_box_id
                  AND NOT deleted;

                INSERT INTO public.user_update_events (
                    user_id, pts, pts_count, date, event_type,
                    message_box_id, peer_type, peer_id
                ) VALUES (
                    pair.owner_user_id, next_pts, 1, event_date, 'edit_message',
                    pair.source_box_id, pair.source_peer_type, pair.source_peer_id
                );

                INSERT INTO public.dispatch_outbox (
                    target_user_id, pts, event_type,
                    exclude_auth_key_id, exclude_session_id
                ) VALUES (pair.owner_user_id, next_pts, 'edit_message', 0, 0);

                IF pair.owner_user_id = gift.owner_peer_id THEN
                    UPDATE public.star_gift_upgrade_commands
                    SET source_edit_pts = next_pts
                    WHERE source_saved_gift_id = gift.id;
                END IF;
            END IF;

            IF pair.unique_media IS DISTINCT FROM repaired_unique_media THEN
                INSERT INTO public.user_update_watermarks (user_id, contiguous_pts)
                VALUES (pair.owner_user_id, 0)
                ON CONFLICT (user_id) DO NOTHING;

                UPDATE public.user_update_watermarks
                SET contiguous_pts = contiguous_pts + 1,
                    updated_at = now()
                WHERE user_id = pair.owner_user_id
                RETURNING contiguous_pts INTO next_pts;

                UPDATE public.message_boxes
                SET media = repaired_unique_media,
                    pts = next_pts
                WHERE owner_user_id = pair.owner_user_id
                  AND box_id = pair.unique_box_id
                  AND NOT deleted;

                INSERT INTO public.user_update_events (
                    user_id, pts, pts_count, date, event_type,
                    message_box_id, peer_type, peer_id
                ) VALUES (
                    pair.owner_user_id, next_pts, 1, event_date, 'edit_message',
                    pair.unique_box_id, pair.unique_peer_type, pair.unique_peer_id
                );

                INSERT INTO public.dispatch_outbox (
                    target_user_id, pts, event_type,
                    exclude_auth_key_id, exclude_session_id
                ) VALUES (pair.owner_user_id, next_pts, 'edit_message', 0, 0);
            END IF;
        END LOOP;

        SELECT media INTO STRICT private_media
        FROM public.message_boxes
        WHERE message_sender_id = source_root.message_sender_id
          AND private_message_id = source_root.private_message_id
          AND NOT deleted
        ORDER BY (owner_user_id = message_sender_id) DESC, owner_user_id
        LIMIT 1;
        UPDATE public.private_messages
        SET media = private_media
        WHERE sender_user_id = source_root.message_sender_id
          AND id = source_root.private_message_id;

        SELECT media INTO STRICT private_media
        FROM public.message_boxes
        WHERE message_sender_id = upgrade_root.message_sender_id
          AND private_message_id = upgrade_root.private_message_id
          AND NOT deleted
        ORDER BY (owner_user_id = message_sender_id) DESC, owner_user_id
        LIMIT 1;
        UPDATE public.private_messages
        SET media = private_media
        WHERE sender_user_id = upgrade_root.message_sender_id
          AND id = upgrade_root.private_message_id;

        IF EXISTS (
            SELECT 1 FROM public.star_gift_upgrade_commands
            WHERE source_saved_gift_id = gift.id AND source_edit_pts <= 0
        ) THEN
            RAISE EXCEPTION 'saved gift % is missing its owner source edit receipt', gift.id;
        END IF;
    END LOOP;
END
$$;
