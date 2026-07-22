DROP TRIGGER IF EXISTS star_gift_catalog_collectible_preview_activation ON public.star_gift_catalog;
DROP FUNCTION IF EXISTS public.telesrv_validate_collectible_preview_activation();

UPDATE public.star_gift_catalog c
SET collectible_revision_id = repair.collectible_revision_id, updated_at = now()
FROM public.star_gift_collectible_preview_repairs repair
WHERE c.gift_id = repair.gift_id
  AND c.collectible_revision_id IS NULL;

DROP TABLE public.star_gift_collectible_preview_repairs;
