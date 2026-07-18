-- Durable lifecycle sweep support. 0095 originally named this column after the
-- first use case (expiry); cancelled offers use the same outbox boundary.
ALTER TABLE public.star_gift_offers
    RENAME COLUMN expiry_notified TO resolution_notified;

CREATE INDEX star_gift_offers_resolution_outbox_idx
    ON public.star_gift_offers(id)
    WHERE status IN ('expired','cancelled') AND NOT resolution_notified;

CREATE INDEX star_gift_auctions_due_idx
    ON public.star_gift_auctions(status, next_round_at, gift_id)
    WHERE status IN ('pending','active');

CREATE INDEX star_gift_auction_acquired_delivery_idx
    ON public.star_gift_auction_acquired(gift_id, id)
    WHERE saved_gift_id IS NULL;
