ALTER TABLE public.star_gift_user_purchases
    DROP CONSTRAINT IF EXISTS star_gift_user_purchases_window_check,
    DROP COLUMN IF EXISTS window_started_at,
    DROP COLUMN IF EXISTS window_count;

ALTER TABLE public.star_gift_catalog_revisions
    DROP CONSTRAINT IF EXISTS star_gift_catalog_revision_rate_limit_check,
    DROP COLUMN IF EXISTS rate_limit_count,
    DROP COLUMN IF EXISTS rate_limit_window_seconds;
