-- payments.deleteStarGift (Layer 232) needs a "deleted" terminal state: the gift
-- leaves the profile page, but the audit history (upgrade commands, offers,
-- transfer records) still references the peer_star_gifts row, so it cannot be hard
-- deleted. Reuse lifecycle_status: add 'deleted'; the read paths already project
-- only 'active', so a deleted gift hides itself naturally. The constraint stays
-- consistent with the code: only a non-upgraded ordinary gift can enter 'deleted'
-- (unique_gift_id IS NULL), and deletion never leaves a converted row behind.

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
