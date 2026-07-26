-- Restore the single-username-per-peer invariant. Collectible registry rows are
-- dropped first so the old unique index can be recreated; the assets and their
-- provenance log are then removed with the tables.

DELETE FROM public.peer_usernames WHERE collectible_id IS NOT NULL OR NOT editable;

DROP INDEX IF EXISTS public.peer_usernames_collectible_idx;
DROP INDEX IF EXISTS public.peer_usernames_peer_order_idx;
DROP INDEX IF EXISTS public.peer_usernames_peer_editable_idx;

ALTER TABLE public.peer_usernames
    DROP CONSTRAINT IF EXISTS peer_usernames_sort_order_check,
    DROP CONSTRAINT IF EXISTS peer_usernames_collectible_not_editable_check,
    DROP CONSTRAINT IF EXISTS peer_usernames_username_case_check;

ALTER TABLE public.peer_usernames
    DROP COLUMN IF EXISTS collectible_id,
    DROP COLUMN IF EXISTS sort_order,
    DROP COLUMN IF EXISTS editable,
    DROP COLUMN IF EXISTS active,
    DROP COLUMN IF EXISTS username;

CREATE UNIQUE INDEX IF NOT EXISTS peer_usernames_peer_unique_idx
    ON public.peer_usernames (peer_type, peer_id);

CREATE OR REPLACE FUNCTION public.delete_user_peer_username() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    DELETE FROM public.peer_usernames WHERE peer_type = 'user' AND peer_id = OLD.id;
    RETURN OLD;
END;
$$;

CREATE OR REPLACE FUNCTION public.delete_channel_peer_username() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    DELETE FROM public.peer_usernames WHERE peer_type = 'channel' AND peer_id = OLD.id;
    RETURN OLD;
END;
$$;

DROP TABLE IF EXISTS public.collectible_username_transfers;
DROP TABLE IF EXISTS public.collectible_usernames;
