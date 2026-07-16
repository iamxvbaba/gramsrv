DROP TRIGGER IF EXISTS star_gift_collectible_backdrop_guard ON public.star_gift_collectible_backdrops;
DROP TRIGGER IF EXISTS star_gift_collectible_pattern_guard ON public.star_gift_collectible_patterns;
DROP TRIGGER IF EXISTS star_gift_collectible_model_guard ON public.star_gift_collectible_models;
DROP TRIGGER IF EXISTS star_gift_collectible_revision_guard ON public.star_gift_collectible_revisions;
DROP FUNCTION IF EXISTS public.telesrv_guard_collectible_attribute();
DROP FUNCTION IF EXISTS public.telesrv_guard_collectible_revision();

DROP TABLE IF EXISTS public.star_gift_collection_items;
DROP TABLE IF EXISTS public.star_gift_collections;
DROP TABLE IF EXISTS public.star_gift_upgrade_commands;

DROP INDEX IF EXISTS public.peer_star_gifts_unique_gift_uniq;
ALTER TABLE public.peer_star_gifts
    DROP CONSTRAINT IF EXISTS peer_star_gifts_pinned_order_check,
    DROP CONSTRAINT IF EXISTS peer_star_gifts_terminal_state_check,
    DROP CONSTRAINT IF EXISTS peer_star_gifts_unique_gift_fk,
    DROP COLUMN IF EXISTS pinned_order,
    DROP COLUMN IF EXISTS upgrade_msg_id,
    DROP COLUMN IF EXISTS unique_gift_id;

DROP TABLE IF EXISTS public.unique_star_gifts;

ALTER TABLE public.star_gift_catalog
    DROP CONSTRAINT IF EXISTS star_gift_catalog_collectible_revision_fk,
    DROP COLUMN IF EXISTS collectible_revision_id;

DROP TABLE IF EXISTS public.star_gift_collectible_backdrops;
DROP TABLE IF EXISTS public.star_gift_collectible_patterns;
DROP TABLE IF EXISTS public.star_gift_collectible_models;
DROP TABLE IF EXISTS public.star_gift_collectible_revisions;
DROP SEQUENCE IF EXISTS public.unique_star_gift_id_seq;
