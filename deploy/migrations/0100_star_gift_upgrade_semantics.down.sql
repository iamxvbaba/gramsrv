UPDATE public.message_boxes
SET media = jsonb_set(media, '{service_action,star_gift,upgrade_stars}', media #> '{service_action,star_gift,upgrade_price_stars}', true)
         #- '{service_action,star_gift,upgrade_price_stars}'
WHERE media #>> '{service_action,kind}' = 'star_gift'
  AND media #> '{service_action,star_gift,upgrade_price_stars}' IS NOT NULL;

UPDATE public.channel_messages
SET action = jsonb_set(action, '{StarGift,upgrade_stars}', action #> '{StarGift,upgrade_price_stars}', true)
             #- '{StarGift,upgrade_price_stars}'
WHERE action #>> '{Type}' = 'star_gift'
  AND action #> '{StarGift,upgrade_price_stars}' IS NOT NULL;

UPDATE public.channel_admin_log_events
SET message = jsonb_set(message, '{Action,StarGift,upgrade_stars}', message #> '{Action,StarGift,upgrade_price_stars}', true)
              #- '{Action,StarGift,upgrade_price_stars}'
WHERE message #>> '{Action,Type}' = 'star_gift'
  AND message #> '{Action,StarGift,upgrade_price_stars}' IS NOT NULL;

UPDATE public.channel_admin_log_events
SET prev_message = jsonb_set(prev_message, '{Action,StarGift,upgrade_stars}', prev_message #> '{Action,StarGift,upgrade_price_stars}', true)
                   #- '{Action,StarGift,upgrade_price_stars}'
WHERE prev_message #>> '{Action,Type}' = 'star_gift'
  AND prev_message #> '{Action,StarGift,upgrade_price_stars}' IS NOT NULL;

UPDATE public.channel_admin_log_events
SET new_message = jsonb_set(new_message, '{Action,StarGift,upgrade_stars}', new_message #> '{Action,StarGift,upgrade_price_stars}', true)
                  #- '{Action,StarGift,upgrade_price_stars}'
WHERE new_message #>> '{Action,Type}' = 'star_gift'
  AND new_message #> '{Action,StarGift,upgrade_price_stars}' IS NOT NULL;

ALTER TABLE public.star_gift_upgrade_commands
    DROP CONSTRAINT IF EXISTS star_gift_upgrade_command_replay_shape_check,
    DROP COLUMN IF EXISTS keep_original_details,
    DROP COLUMN IF EXISTS require_prepaid,
    DROP COLUMN IF EXISTS charge_stars;
