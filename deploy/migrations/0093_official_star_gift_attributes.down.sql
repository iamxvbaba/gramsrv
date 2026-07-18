DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM public.star_gift_collectible_models
        WHERE rarity_kind <> 'permille' OR crafted
    ) THEN
        RAISE EXCEPTION 'cannot downgrade while categorical/crafted collectible models exist';
    END IF;
END;
$$;

DROP INDEX IF EXISTS public.star_gift_catalog_revisions_official_source_idx;

ALTER TABLE public.star_gift_collectible_backdrops
    DROP CONSTRAINT star_gift_collectible_backdrop_rarity_check,
    ALTER COLUMN rarity_permille SET NOT NULL,
    DROP COLUMN rarity_kind,
    ADD CONSTRAINT star_gift_collectible_backdrop_rarity_check CHECK (rarity_permille BETWEEN 1 AND 1000);

ALTER TABLE public.star_gift_collectible_patterns
    DROP CONSTRAINT star_gift_collectible_pattern_official_document_check,
    DROP CONSTRAINT star_gift_collectible_pattern_rarity_check,
    ALTER COLUMN rarity_permille SET NOT NULL,
    DROP COLUMN official_document_id,
    DROP COLUMN rarity_kind,
    ADD CONSTRAINT star_gift_collectible_pattern_rarity_check CHECK (rarity_permille BETWEEN 1 AND 1000);

ALTER TABLE public.star_gift_collectible_models
    DROP CONSTRAINT star_gift_collectible_model_official_document_check,
    DROP CONSTRAINT star_gift_collectible_model_rarity_check,
    ALTER COLUMN rarity_permille SET NOT NULL,
    DROP COLUMN official_document_id,
    DROP COLUMN crafted,
    DROP COLUMN rarity_kind,
    ADD CONSTRAINT star_gift_collectible_model_rarity_check CHECK (rarity_permille BETWEEN 1 AND 1000);

ALTER TABLE public.star_gift_collectible_revisions
    DROP CONSTRAINT star_gift_collectible_official_source_check,
    DROP COLUMN source_manifest_sha256,
    DROP COLUMN official_gift_id;

ALTER TABLE public.star_gift_catalog_revisions
    DROP CONSTRAINT star_gift_catalog_official_source_check,
    DROP COLUMN official_source,
    DROP COLUMN source_manifest_sha256,
    DROP COLUMN official_gift_id;

CREATE OR REPLACE FUNCTION public.telesrv_guard_collectible_revision() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status = 'published' THEN
            RAISE EXCEPTION 'published collectible revision is immutable';
        END IF;
        RETURN OLD;
    END IF;

    IF OLD.status = 'published' THEN
        IF NEW.gift_id <> OLD.gift_id OR NEW.revision <> OLD.revision OR
           NEW.upgrade_stars <> OLD.upgrade_stars OR NEW.supply_total <> OLD.supply_total OR
           NEW.slug_prefix <> OLD.slug_prefix OR NEW.status <> OLD.status OR
           NEW.created_by <> OLD.created_by OR NEW.command_id <> OLD.command_id OR
           NEW.created_at <> OLD.created_at OR NEW.published_at <> OLD.published_at THEN
            RAISE EXCEPTION 'published collectible revision is immutable';
        END IF;
        IF NEW.issued <> OLD.issued + 1 THEN
            RAISE EXCEPTION 'published collectible issuance must advance exactly once';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
