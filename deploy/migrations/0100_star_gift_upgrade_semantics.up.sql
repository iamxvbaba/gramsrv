-- Split the two TL fields that TDesktop consumes differently:
--   StarGift.upgrade_stars              = current paid-upgrade price
--   messageActionStarGift.upgrade_stars = amount already prepaid by sender
-- Also persist the immutable command envelope required to replay an upgrade
-- after the saved gift has entered its unique terminal state.

ALTER TABLE public.star_gift_upgrade_commands
    ADD COLUMN charge_stars bigint NOT NULL DEFAULT 0,
    ADD COLUMN require_prepaid boolean NOT NULL DEFAULT false,
    ADD COLUMN keep_original_details boolean NOT NULL DEFAULT false;

UPDATE public.star_gift_upgrade_commands c
SET charge_stars = CASE WHEN c.form_id = 0 THEN 0 ELSE r.upgrade_stars END,
    require_prepaid = (c.form_id = 0),
    keep_original_details = u.keep_original_details
FROM public.unique_star_gifts u
JOIN public.star_gift_collectible_revisions r ON r.id = u.collectible_revision_id
WHERE u.id = c.unique_gift_id;

ALTER TABLE public.star_gift_upgrade_commands
    ADD CONSTRAINT star_gift_upgrade_command_replay_shape_check CHECK (
        (require_prepaid AND form_id = 0 AND charge_stars = 0)
        OR
        (NOT require_prepaid AND form_id <> 0 AND charge_stars > 0)
    );

-- Private message boxes are the canonical durable snapshots used by history,
-- live outbox delivery and updates.getDifference.
UPDATE public.message_boxes
SET media = CASE
    WHEN COALESCE((media #>> '{service_action,star_gift,prepaid_upgrade}')::boolean, false)
        THEN jsonb_set(media, '{service_action,star_gift,upgrade_price_stars}', media #> '{service_action,star_gift,upgrade_stars}', true)
    ELSE jsonb_set(media, '{service_action,star_gift,upgrade_price_stars}', media #> '{service_action,star_gift,upgrade_stars}', true)
         #- '{service_action,star_gift,upgrade_stars}'
END
WHERE media #>> '{service_action,kind}' = 'star_gift'
  AND media #> '{service_action,star_gift,upgrade_stars}' IS NOT NULL;

-- Channel service-message and Recent Actions snapshots use exported Go field
-- names for their outer objects and the same snake_case StarGift payload.
UPDATE public.channel_messages
SET action = CASE
    WHEN COALESCE((action #>> '{StarGift,prepaid_upgrade}')::boolean, false)
        THEN jsonb_set(action, '{StarGift,upgrade_price_stars}', action #> '{StarGift,upgrade_stars}', true)
    ELSE jsonb_set(action, '{StarGift,upgrade_price_stars}', action #> '{StarGift,upgrade_stars}', true)
         #- '{StarGift,upgrade_stars}'
END
WHERE action #>> '{Type}' = 'star_gift'
  AND action #> '{StarGift,upgrade_stars}' IS NOT NULL;

UPDATE public.channel_admin_log_events
SET message = CASE
    WHEN COALESCE((message #>> '{Action,StarGift,prepaid_upgrade}')::boolean, false)
        THEN jsonb_set(message, '{Action,StarGift,upgrade_price_stars}', message #> '{Action,StarGift,upgrade_stars}', true)
    ELSE jsonb_set(message, '{Action,StarGift,upgrade_price_stars}', message #> '{Action,StarGift,upgrade_stars}', true)
         #- '{Action,StarGift,upgrade_stars}'
END
WHERE message #>> '{Action,Type}' = 'star_gift'
  AND message #> '{Action,StarGift,upgrade_stars}' IS NOT NULL;

UPDATE public.channel_admin_log_events
SET prev_message = CASE
    WHEN COALESCE((prev_message #>> '{Action,StarGift,prepaid_upgrade}')::boolean, false)
        THEN jsonb_set(prev_message, '{Action,StarGift,upgrade_price_stars}', prev_message #> '{Action,StarGift,upgrade_stars}', true)
    ELSE jsonb_set(prev_message, '{Action,StarGift,upgrade_price_stars}', prev_message #> '{Action,StarGift,upgrade_stars}', true)
         #- '{Action,StarGift,upgrade_stars}'
END
WHERE prev_message #>> '{Action,Type}' = 'star_gift'
  AND prev_message #> '{Action,StarGift,upgrade_stars}' IS NOT NULL;

UPDATE public.channel_admin_log_events
SET new_message = CASE
    WHEN COALESCE((new_message #>> '{Action,StarGift,prepaid_upgrade}')::boolean, false)
        THEN jsonb_set(new_message, '{Action,StarGift,upgrade_price_stars}', new_message #> '{Action,StarGift,upgrade_stars}', true)
    ELSE jsonb_set(new_message, '{Action,StarGift,upgrade_price_stars}', new_message #> '{Action,StarGift,upgrade_stars}', true)
         #- '{Action,StarGift,upgrade_stars}'
END
WHERE new_message #>> '{Action,Type}' = 'star_gift'
  AND new_message #> '{Action,StarGift,upgrade_stars}' IS NOT NULL;
