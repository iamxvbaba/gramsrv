-- v2 编号 0251；移植自 main 0222_star_gift_profile_date_order（main 3a84827a，B7b–7e），见 docs/main-v2-sync.md。
-- 资料页礼物改为按收到时刻（gift_date）倒序：礼物实例行在所有权转移时被复用，
-- gift_date 会被重置为转移时刻，而 id 保持不变，按 id 排序时被转送的礼物
-- 会停在旧位置而不是列表顶部。
DROP INDEX IF EXISTS public.peer_star_gifts_owner_profile_order_idx;

CREATE INDEX peer_star_gifts_owner_profile_order_idx
    ON public.peer_star_gifts(
        owner_peer_type,
        owner_peer_id,
        (pinned_order = 0),
        pinned_order,
        gift_date DESC,
        id DESC
    )
    WHERE lifecycle_status = 'active';
