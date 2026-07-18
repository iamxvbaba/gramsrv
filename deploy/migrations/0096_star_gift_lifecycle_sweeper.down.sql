DROP INDEX IF EXISTS public.star_gift_auction_acquired_delivery_idx;
DROP INDEX IF EXISTS public.star_gift_auctions_due_idx;
DROP INDEX IF EXISTS public.star_gift_offers_resolution_outbox_idx;

ALTER TABLE public.star_gift_offers
    RENAME COLUMN resolution_notified TO expiry_notified;
