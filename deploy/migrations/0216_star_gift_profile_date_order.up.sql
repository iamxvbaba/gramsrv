-- Profile gifts are now ordered by receipt time (gift_date) descending: a gift
-- instance row is reused on ownership transfer, gift_date is reset to the transfer
-- time, and id stays unchanged, so under id ordering a re-gifted gift would stay in
-- its old position instead of the top of the list.
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
