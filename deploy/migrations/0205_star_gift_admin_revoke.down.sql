ALTER TABLE public.peer_star_gifts DROP CONSTRAINT peer_star_gifts_lifecycle_check;
ALTER TABLE public.peer_star_gifts ADD CONSTRAINT peer_star_gifts_lifecycle_check CHECK (
    (lifecycle_status = ANY (ARRAY['active'::text, 'converted'::text, 'burned'::text, 'exported'::text]))
    AND (transfer_stars >= 0) AND (gift_num >= 0) AND (can_export_at >= 0) AND (can_transfer_at >= 0)
    AND (can_resell_at >= 0) AND (drop_original_details_stars >= 0) AND (can_craft_at >= 0)
    AND (
        ((lifecycle_status = 'converted'::text) AND converted AND (unique_gift_id IS NULL))
        OR ((lifecycle_status = 'active'::text) AND (NOT converted))
        OR ((lifecycle_status = ANY (ARRAY['burned'::text, 'exported'::text])) AND (NOT converted) AND (unique_gift_id IS NOT NULL))
    )
);
