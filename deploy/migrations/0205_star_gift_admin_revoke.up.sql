-- Admin panel "delete gift everywhere" needs a terminal peer_star_gifts state
-- for a REGULAR (never-upgraded, unique_gift_id IS NULL) instance. The
-- existing terminal states don't fit: 'burned'/'exported' both require
-- unique_gift_id IS NOT NULL (they are outcomes of the craft/export flow on
-- an already-unique gift), and 'converted' means the OWNER chose to convert
-- it back to Stars, which is a different fact than an operator revoking it.
-- 'revoked' is admin-initiated removal, distinct from every user-initiated
-- terminal state, and -- like 'burned'/'exported' -- excluded from every
-- existing "active gifts" read path (they all filter on
-- lifecycle_status = 'active' explicitly).
--
-- An already-upgraded instance being deleted still goes through the
-- existing, proven 'burned' path (see star_gift_craft_auction.go's own burn
-- code) -- this migration only fills the gap for a regular instance.
ALTER TABLE public.peer_star_gifts DROP CONSTRAINT peer_star_gifts_lifecycle_check;
ALTER TABLE public.peer_star_gifts ADD CONSTRAINT peer_star_gifts_lifecycle_check CHECK (
    (lifecycle_status = ANY (ARRAY['active'::text, 'converted'::text, 'burned'::text, 'exported'::text, 'revoked'::text]))
    AND (transfer_stars >= 0) AND (gift_num >= 0) AND (can_export_at >= 0) AND (can_transfer_at >= 0)
    AND (can_resell_at >= 0) AND (drop_original_details_stars >= 0) AND (can_craft_at >= 0)
    AND (
        ((lifecycle_status = 'converted'::text) AND converted AND (unique_gift_id IS NULL))
        OR ((lifecycle_status = 'active'::text) AND (NOT converted))
        OR ((lifecycle_status = ANY (ARRAY['burned'::text, 'exported'::text])) AND (NOT converted) AND (unique_gift_id IS NOT NULL))
        OR ((lifecycle_status = 'revoked'::text) AND (NOT converted))
    )
);
