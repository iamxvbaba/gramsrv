-- Preserve the complete Layer 228 official collectible attribute shape. Display rarity is
-- distinct from regular-upgrade selection eligibility, and official provenance is recorded
-- on both immutable revisions created by one import command.

ALTER TABLE public.star_gift_catalog_revisions
    ADD COLUMN official_gift_id bigint,
    ADD COLUMN source_manifest_sha256 bytea,
	ADD COLUMN official_source jsonb,
    ADD CONSTRAINT star_gift_catalog_official_source_check CHECK (
		(official_gift_id IS NULL AND source_manifest_sha256 IS NULL AND official_source IS NULL) OR
		(official_gift_id > 0 AND source_manifest_sha256 IS NOT NULL AND official_source IS NOT NULL AND
		 octet_length(source_manifest_sha256) = 32 AND jsonb_typeof(official_source) = 'object')
    );

ALTER TABLE public.star_gift_collectible_revisions
    ADD COLUMN official_gift_id bigint,
    ADD COLUMN source_manifest_sha256 bytea,
    ADD CONSTRAINT star_gift_collectible_official_source_check CHECK (
        (official_gift_id IS NULL AND source_manifest_sha256 IS NULL) OR
        (official_gift_id > 0 AND source_manifest_sha256 IS NOT NULL AND octet_length(source_manifest_sha256) = 32)
    );

ALTER TABLE public.star_gift_collectible_models
    DROP CONSTRAINT star_gift_collectible_model_rarity_check,
    ALTER COLUMN rarity_permille DROP NOT NULL,
    ADD COLUMN rarity_kind text DEFAULT 'permille' NOT NULL,
    ADD COLUMN crafted boolean DEFAULT false NOT NULL,
    ADD COLUMN official_document_id bigint,
    ADD CONSTRAINT star_gift_collectible_model_rarity_check CHECK (
        rarity_kind IN ('permille', 'uncommon', 'rare', 'epic', 'legendary') AND
        ((rarity_kind = 'permille' AND rarity_permille BETWEEN 1 AND 1000 AND NOT crafted) OR
         (rarity_kind <> 'permille' AND rarity_permille IS NULL AND crafted))
    ),
    ADD CONSTRAINT star_gift_collectible_model_official_document_check CHECK (
        official_document_id IS NULL OR official_document_id > 0
    );

ALTER TABLE public.star_gift_collectible_patterns
    DROP CONSTRAINT star_gift_collectible_pattern_rarity_check,
    ALTER COLUMN rarity_permille DROP NOT NULL,
    ADD COLUMN rarity_kind text DEFAULT 'permille' NOT NULL,
    ADD COLUMN official_document_id bigint,
    ADD CONSTRAINT star_gift_collectible_pattern_rarity_check CHECK (
        rarity_kind = 'permille' AND rarity_permille BETWEEN 1 AND 1000
    ),
    ADD CONSTRAINT star_gift_collectible_pattern_official_document_check CHECK (
        official_document_id IS NULL OR official_document_id > 0
    );

ALTER TABLE public.star_gift_collectible_backdrops
    DROP CONSTRAINT star_gift_collectible_backdrop_rarity_check,
    ALTER COLUMN rarity_permille DROP NOT NULL,
    ADD COLUMN rarity_kind text DEFAULT 'permille' NOT NULL,
    ADD CONSTRAINT star_gift_collectible_backdrop_rarity_check CHECK (
        rarity_kind = 'permille' AND rarity_permille BETWEEN 1 AND 1000
    );

CREATE INDEX star_gift_catalog_revisions_official_source_idx
    ON public.star_gift_catalog_revisions(official_gift_id, id DESC)
    WHERE official_gift_id IS NOT NULL;

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
           NEW.created_at <> OLD.created_at OR NEW.published_at <> OLD.published_at OR
           NEW.official_gift_id IS DISTINCT FROM OLD.official_gift_id OR
           NEW.source_manifest_sha256 IS DISTINCT FROM OLD.source_manifest_sha256 THEN
            RAISE EXCEPTION 'published collectible revision is immutable';
        END IF;
        IF NEW.issued <> OLD.issued + 1 THEN
            RAISE EXCEPTION 'published collectible issuance must advance exactly once';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
