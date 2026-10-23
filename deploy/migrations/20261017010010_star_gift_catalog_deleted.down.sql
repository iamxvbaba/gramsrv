ALTER TABLE public.star_gift_catalog
    DROP CONSTRAINT IF EXISTS star_gift_catalog_deleted_disabled_check,
    DROP COLUMN IF EXISTS deleted_at;
