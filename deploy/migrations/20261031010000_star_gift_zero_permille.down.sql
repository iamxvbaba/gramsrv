-- Restores the old "weight must be >= 1" constraint. If official attributes with permille=0 have
-- already been imported, this migration aborts on the CHECK failure: delete those attributes
-- (or set them back to an extractable weight) before rolling back.
ALTER TABLE public.star_gift_collectible_models
    DROP CONSTRAINT star_gift_collectible_model_rarity_check,
    ADD CONSTRAINT star_gift_collectible_model_rarity_check CHECK (
        rarity_kind IN ('permille', 'uncommon', 'rare', 'epic', 'legendary') AND
        ((rarity_kind = 'permille' AND rarity_permille BETWEEN 1 AND 1000 AND NOT crafted) OR
         (rarity_kind <> 'permille' AND rarity_permille IS NULL AND crafted))
    );

ALTER TABLE public.star_gift_collectible_patterns
    DROP CONSTRAINT star_gift_collectible_pattern_rarity_check,
    ADD CONSTRAINT star_gift_collectible_pattern_rarity_check CHECK (
        rarity_kind = 'permille' AND rarity_permille BETWEEN 1 AND 1000
    );

ALTER TABLE public.star_gift_collectible_backdrops
    DROP CONSTRAINT star_gift_collectible_backdrop_rarity_check,
    ADD CONSTRAINT star_gift_collectible_backdrop_rarity_check CHECK (
        rarity_kind = 'permille' AND rarity_permille BETWEEN 1 AND 1000
    );

-- Restores the 0219 activation trigger: 0-weight attributes count toward the preview pool again.
CREATE OR REPLACE FUNCTION public.telesrv_validate_collectible_preview_activation() RETURNS trigger
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
           AND rarity_kind = 'permille' AND NOT crafted) < 1 THEN
        RAISE EXCEPTION 'collectible model preview requires a document'
            USING ERRCODE = '23514';
    END IF;
    IF (SELECT count(DISTINCT document_id)
          FROM public.star_gift_collectible_patterns
         WHERE collectible_revision_id = NEW.collectible_revision_id
           AND rarity_kind = 'permille') < 1 THEN
        RAISE EXCEPTION 'collectible pattern preview requires a document'
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
