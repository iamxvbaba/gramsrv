ALTER TABLE public.peer_star_gifts
    DROP CONSTRAINT IF EXISTS peer_star_gifts_prepaid_upgrade_check,
    DROP COLUMN IF EXISTS prepaid_upgrade_stars;
