-- Collectible slugs keep their case, like Telegram's "PlushPepe-123". Lookups
-- compare lower(slug), so uniqueness has to hold case-insensitively too, and
-- the index serves those lookups.
ALTER TABLE public.star_gift_collectible_revisions
    DROP CONSTRAINT IF EXISTS star_gift_collectible_slug_check,
    ADD CONSTRAINT star_gift_collectible_slug_check CHECK (slug_prefix ~ '^[A-Za-z0-9][A-Za-z0-9-]{0,47}$');

CREATE UNIQUE INDEX IF NOT EXISTS unique_star_gifts_slug_lower_uniq ON public.unique_star_gifts (lower(slug));
