-- v2 编号 0233；移植自 main 0204_star_gift_support_only（main 8bdd708a），见 docs/main-v2-sync.md。
-- Restrict a catalog gift to official-support accounts only. The flag lives on
-- the immutable catalog revision (like require_premium) and is enforced at the
-- RPC purchase boundary: buyers without the support account flag are treated as
-- sold out (STARGIFT_USAGE_LIMITED on sendStarsForm, sold-out ResultFail on
-- checkCanSendGift) and cannot form an auction bid (STARGIFT_INVALID).

ALTER TABLE public.star_gift_catalog_revisions
    ADD COLUMN support_only boolean DEFAULT false NOT NULL;