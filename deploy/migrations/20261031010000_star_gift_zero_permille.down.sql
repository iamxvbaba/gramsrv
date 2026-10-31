-- 恢复「权重必须 >= 1」的旧约束。若库里已经导入过 permille=0 的官方属性，本迁移会
-- 因 CHECK 失败而中止：先删除这些属性（或把它们改回可抽取的权重）再回滚。
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

-- 恢复 0219 的激活触发器：0 权重属性重新计入预览池。
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
