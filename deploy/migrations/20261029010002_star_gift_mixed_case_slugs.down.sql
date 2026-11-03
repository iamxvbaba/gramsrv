DROP INDEX IF EXISTS public.unique_star_gifts_slug_lower_uniq;

ALTER TABLE public.star_gift_collectible_revisions
    DROP CONSTRAINT star_gift_collectible_slug_check,
    ADD CONSTRAINT star_gift_collectible_slug_check CHECK (slug_prefix ~ '^[a-z0-9][a-z0-9-]{0,47}$');
