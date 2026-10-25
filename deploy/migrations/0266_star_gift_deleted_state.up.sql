-- v2 编号 0266；移植自 main 0242_star_gift_deleted_state（main 2cd80216，B15），见 docs/main-v2-sync.md。
-- payments.deleteStarGift (Layer 232) 需要一个「已删除」终态：礼物离开资料页，
-- 但审计历史（升级命令、报价、转账记录）仍引用 peer_star_gifts 行，所以不能硬删。
-- 复用 lifecycle_status：新增 'deleted'，读路径本来就只投影 'active'，删除后自然隐身。
-- 约束和代码保持一致：只有未升级的普通礼物能进入 'deleted'（unique_gift_id IS NULL），
-- 删除也永远不会留下 converted 行。

ALTER TABLE public.peer_star_gifts
    DROP CONSTRAINT IF EXISTS peer_star_gifts_lifecycle_check,
    ADD CONSTRAINT peer_star_gifts_lifecycle_check CHECK (
        lifecycle_status IN ('active', 'converted', 'burned', 'exported', 'deleted') AND
        transfer_stars >= 0 AND gift_num >= 0 AND can_export_at >= 0 AND can_transfer_at >= 0 AND
        can_resell_at >= 0 AND drop_original_details_stars >= 0 AND can_craft_at >= 0 AND
        ((lifecycle_status='converted' AND converted AND unique_gift_id IS NULL) OR
         (lifecycle_status='active' AND NOT converted) OR
         (lifecycle_status IN ('burned','exported') AND NOT converted AND unique_gift_id IS NOT NULL) OR
         (lifecycle_status='deleted' AND NOT converted AND unique_gift_id IS NULL))
    );
