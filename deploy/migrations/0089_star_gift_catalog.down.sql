DROP TRIGGER IF EXISTS star_gift_catalog_changed ON public.star_gift_catalog;
DROP FUNCTION IF EXISTS public.telesrv_notify_star_gift_catalog_changed();

ALTER TABLE public.peer_star_gifts
    DROP CONSTRAINT IF EXISTS peer_star_gifts_catalog_revision_fk,
    DROP COLUMN IF EXISTS catalog_revision_id;
DROP INDEX IF EXISTS public.peer_star_gifts_gift_idx;

ALTER TABLE public.star_gift_catalog
    DROP CONSTRAINT IF EXISTS star_gift_catalog_active_revision_fk;
DROP TABLE IF EXISTS public.star_gift_catalog_revisions;
DROP TABLE IF EXISTS public.star_gift_catalog;
DROP SEQUENCE IF EXISTS public.star_gift_catalog_revision_id_seq;
DROP SEQUENCE IF EXISTS public.star_gift_catalog_gift_id_seq;

DELETE FROM public.read_model_versions
WHERE model = 'star_gift_catalog' AND owner_user_id = 0 AND peer_type = '' AND peer_id = 0;
