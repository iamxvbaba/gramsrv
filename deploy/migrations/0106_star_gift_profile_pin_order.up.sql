CREATE UNIQUE INDEX peer_star_gifts_owner_pinned_order_uniq
    ON public.peer_star_gifts(owner_peer_type, owner_peer_id, pinned_order)
    WHERE pinned_order > 0;

CREATE INDEX peer_star_gifts_owner_profile_order_idx
    ON public.peer_star_gifts(
        owner_peer_type,
        owner_peer_id,
        (pinned_order = 0),
        pinned_order,
        id DESC
    )
    WHERE lifecycle_status = 'active';
