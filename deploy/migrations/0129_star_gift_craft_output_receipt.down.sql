ALTER TABLE public.star_gift_craft_commands
    DROP CONSTRAINT IF EXISTS star_gift_craft_output_receipt_check,
    DROP COLUMN IF EXISTS output_fingerprint,
    DROP COLUMN IF EXISTS output_media;
