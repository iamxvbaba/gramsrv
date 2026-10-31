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
