-- Restoring the global unique constraint requires at most one row per name.
-- Reissued names keep their newest row, which is the live asset when one exists.
DELETE FROM public.collectible_usernames c
USING public.collectible_usernames newer
WHERE c.username_lower = newer.username_lower
  AND c.id < newer.id;

DROP INDEX IF EXISTS public.collectible_usernames_name_history_idx;
DROP INDEX IF EXISTS public.collectible_usernames_live_name_idx;

ALTER TABLE public.collectible_usernames
    ADD CONSTRAINT collectible_usernames_username_lower_key UNIQUE (username_lower);
