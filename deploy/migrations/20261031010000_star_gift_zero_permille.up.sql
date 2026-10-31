-- Telegram sends permille=0 attributes for sold-out limited gifts: they still belong to the gift pool
-- (keeping them is required to render existing collectibles) but are never randomly drawn. Loosen the
-- CHECK to accept 0; the draw path already filters rarity_permille <= 0, so these attributes are never drawn.
ALTER TABLE public.star_gift_collectible_models
    DROP CONSTRAINT star_gift_collectible_model_rarity_check,
    ADD CONSTRAINT star_gift_collectible_model_rarity_check CHECK (
        rarity_kind IN ('permille', 'uncommon', 'rare', 'epic', 'legendary') AND
        ((rarity_kind = 'permille' AND rarity_permille BETWEEN 0 AND 1000 AND NOT crafted) OR
         (rarity_kind <> 'permille' AND rarity_permille IS NULL AND crafted))
    );

ALTER TABLE public.star_gift_collectible_patterns
    DROP CONSTRAINT star_gift_collectible_pattern_rarity_check,
    ADD CONSTRAINT star_gift_collectible_pattern_rarity_check CHECK (
        rarity_kind = 'permille' AND rarity_permille BETWEEN 0 AND 1000
    );

ALTER TABLE public.star_gift_collectible_backdrops
    DROP CONSTRAINT star_gift_collectible_backdrop_rarity_check,
    ADD CONSTRAINT star_gift_collectible_backdrop_rarity_check CHECK (
        rarity_kind = 'permille' AND rarity_permille BETWEEN 0 AND 1000
    );

-- The activation trigger counts only attributes with weight > 0 toward the preview pool: upgrade preview
-- sampling and the draw both skip 0-weight attributes. Otherwise a revision with one drawable backdrop plus
-- several 0-weight backdrops could still activate, leaving only one backdrop in the preview and making the
-- TDesktop upgrade animation never stop (see 0132/0219).
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
           AND rarity_kind = 'permille' AND rarity_permille > 0 AND NOT crafted) < 1 THEN
        RAISE EXCEPTION 'collectible model preview requires a document'
            USING ERRCODE = '23514';
    END IF;
    IF (SELECT count(DISTINCT document_id)
          FROM public.star_gift_collectible_patterns
         WHERE collectible_revision_id = NEW.collectible_revision_id
           AND rarity_kind = 'permille' AND rarity_permille > 0) < 1 THEN
        RAISE EXCEPTION 'collectible pattern preview requires a document'
            USING ERRCODE = '23514';
    END IF;
    IF (SELECT count(DISTINCT backdrop_id)
          FROM public.star_gift_collectible_backdrops
         WHERE collectible_revision_id = NEW.collectible_revision_id
           AND rarity_kind = 'permille' AND rarity_permille > 0) < 2 THEN
        RAISE EXCEPTION 'collectible backdrop preview requires two distinct IDs'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
