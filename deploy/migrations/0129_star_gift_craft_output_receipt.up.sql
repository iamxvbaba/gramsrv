-- A successful Craft outcome and its self-service message are separated by a
-- process boundary. Freeze the exact output intent in the outcome receipt so
-- retries never rebuild a different message from mutable gift/profile state.
ALTER TABLE public.star_gift_craft_commands
    ADD COLUMN output_media jsonb,
    ADD COLUMN output_fingerprint bytea;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.star_gift_craft_commands command
        WHERE command.success
          AND 1 <> (
              SELECT COUNT(*)
              FROM public.private_messages message
              WHERE message.sender_user_id = command.user_id
                AND message.recipient_user_id = command.user_id
                AND message.sender_snapshot #>> '{message,Media,service_action,kind}' = 'star_gift_unique'
                AND message.sender_snapshot #>> '{message,Media,service_action,star_gift_unique,gift,ID}' = command.result_unique_gift_id::text
                AND COALESCE((message.sender_snapshot #>> '{message,Media,service_action,star_gift_unique,craft}')::boolean, false)
                AND octet_length(message.request_fingerprint) = 32
          )
    ) THEN
        RAISE EXCEPTION 'successful craft command is missing its exact immutable output receipt';
    END IF;
END
$$;

WITH outputs AS (
    SELECT command.user_id,
           command.command_key,
           message.sender_snapshot #> '{message,Media}' AS media,
           message.request_fingerprint
    FROM public.star_gift_craft_commands command
    JOIN public.private_messages message
      ON message.sender_user_id = command.user_id
     AND message.recipient_user_id = command.user_id
     AND message.sender_snapshot #>> '{message,Media,service_action,kind}' = 'star_gift_unique'
     AND message.sender_snapshot #>> '{message,Media,service_action,star_gift_unique,gift,ID}' = command.result_unique_gift_id::text
     AND COALESCE((message.sender_snapshot #>> '{message,Media,service_action,star_gift_unique,craft}')::boolean, false)
    WHERE command.success
)
UPDATE public.star_gift_craft_commands command
SET output_media = output.media,
    output_fingerprint = output.request_fingerprint
FROM outputs output
WHERE command.user_id = output.user_id
  AND command.command_key = output.command_key;

ALTER TABLE public.star_gift_craft_commands
    ADD CONSTRAINT star_gift_craft_output_receipt_check CHECK (
        (success
         AND result_unique_gift_id IS NOT NULL
         AND output_media IS NOT NULL
         AND output_media #>> '{service_action,kind}' = 'star_gift_unique'
         AND COALESCE((output_media #>> '{service_action,star_gift_unique,craft}')::boolean, false)
         AND output_media #>> '{service_action,star_gift_unique,gift,ID}' = result_unique_gift_id::text
         AND octet_length(output_fingerprint) = 32)
        OR
        (NOT success
         AND result_unique_gift_id IS NULL
         AND output_media IS NULL
         AND output_fingerprint IS NULL)
    );
