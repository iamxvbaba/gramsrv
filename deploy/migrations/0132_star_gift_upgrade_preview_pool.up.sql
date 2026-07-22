-- TDesktop deduplicates upgrade-preview models/patterns by document identity and can only
-- finish each attribute spinner after it has a non-target item. Detach previously published
-- pools that cannot satisfy that client contract; the immutable revisions remain available
-- for audit and for already-issued unique gifts.
CREATE TABLE public.star_gift_collectible_preview_repairs (
    gift_id bigint PRIMARY KEY REFERENCES public.star_gift_catalog(gift_id) ON DELETE CASCADE,
    collectible_revision_id bigint UNIQUE NOT NULL
        REFERENCES public.star_gift_collectible_revisions(id) ON DELETE RESTRICT,
    reason text DEFAULT 'insufficient distinct upgrade preview attributes' NOT NULL,
    repaired_at timestamp with time zone DEFAULT now() NOT NULL
);

INSERT INTO public.star_gift_collectible_preview_repairs (gift_id, collectible_revision_id)
SELECT c.gift_id, c.collectible_revision_id
FROM public.star_gift_catalog c
JOIN public.star_gift_collectible_revisions r ON r.id = c.collectible_revision_id
WHERE c.collectible_revision_id IS NOT NULL
  AND (
      r.status <> 'published' OR r.gift_id <> c.gift_id OR
      (SELECT count(DISTINCT m.document_id)
         FROM public.star_gift_collectible_models m
        WHERE m.collectible_revision_id = r.id
          AND m.rarity_kind = 'permille' AND NOT m.crafted) < 2 OR
      (SELECT count(DISTINCT p.document_id)
         FROM public.star_gift_collectible_patterns p
        WHERE p.collectible_revision_id = r.id
          AND p.rarity_kind = 'permille') < 2 OR
      (SELECT count(DISTINCT b.backdrop_id)
         FROM public.star_gift_collectible_backdrops b
        WHERE b.collectible_revision_id = r.id
          AND b.rarity_kind = 'permille') < 2
  );

UPDATE public.star_gift_catalog c
SET collectible_revision_id = NULL, updated_at = now()
FROM public.star_gift_collectible_preview_repairs repair
WHERE c.gift_id = repair.gift_id
  AND c.collectible_revision_id = repair.collectible_revision_id;

-- Keep the same invariant at the final activation boundary. Application validation gives the
-- operator a precise error first; this trigger also protects imports or maintenance SQL that
-- attempts to expose a malformed published revision directly.
CREATE FUNCTION public.telesrv_validate_collectible_preview_activation() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    revision_gift_id bigint;
    revision_status text;
BEGIN
    IF NEW.collectible_revision_id IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT gift_id, status INTO revision_gift_id, revision_status
    FROM public.star_gift_collectible_revisions
    WHERE id = NEW.collectible_revision_id;

    IF NOT FOUND OR revision_gift_id <> NEW.gift_id OR revision_status <> 'published' THEN
        RAISE EXCEPTION 'collectible preview revision must be published for the same gift'
            USING ERRCODE = '23514';
    END IF;
    IF (SELECT count(DISTINCT document_id)
          FROM public.star_gift_collectible_models
         WHERE collectible_revision_id = NEW.collectible_revision_id
           AND rarity_kind = 'permille' AND NOT crafted) < 2 THEN
        RAISE EXCEPTION 'collectible model preview requires two distinct documents'
            USING ERRCODE = '23514';
    END IF;
    IF (SELECT count(DISTINCT document_id)
          FROM public.star_gift_collectible_patterns
         WHERE collectible_revision_id = NEW.collectible_revision_id
           AND rarity_kind = 'permille') < 2 THEN
        RAISE EXCEPTION 'collectible pattern preview requires two distinct documents'
            USING ERRCODE = '23514';
    END IF;
    IF (SELECT count(DISTINCT backdrop_id)
          FROM public.star_gift_collectible_backdrops
         WHERE collectible_revision_id = NEW.collectible_revision_id
           AND rarity_kind = 'permille') < 2 THEN
        RAISE EXCEPTION 'collectible backdrop preview requires two distinct IDs'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER star_gift_catalog_collectible_preview_activation
    BEFORE INSERT OR UPDATE OF collectible_revision_id ON public.star_gift_catalog
    FOR EACH ROW EXECUTE FUNCTION public.telesrv_validate_collectible_preview_activation();
