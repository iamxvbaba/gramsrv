-- Deleted gifts have no earlier representation; restore them as active rows.
UPDATE public.peer_star_gifts SET lifecycle_status='active' WHERE lifecycle_status='deleted';
ALTER TABLE public.peer_star_gifts
    DROP CONSTRAINT peer_star_gifts_lifecycle_check,
    ADD CONSTRAINT peer_star_gifts_lifecycle_check CHECK (
        lifecycle_status IN ('active', 'converted', 'burned', 'exported') AND
        transfer_stars >= 0 AND gift_num >= 0 AND can_export_at >= 0 AND can_transfer_at >= 0 AND
        can_resell_at >= 0 AND drop_original_details_stars >= 0 AND can_craft_at >= 0 AND
        ((lifecycle_status='converted' AND converted AND unique_gift_id IS NULL) OR
         (lifecycle_status='active' AND NOT converted) OR
         (lifecycle_status IN ('burned','exported') AND NOT converted AND unique_gift_id IS NOT NULL))
    );
