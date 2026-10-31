-- Freezing an account unlists all of its market listings.
-- 
-- The listing row is not deleted: the price, currency and listing time must survive
-- the freeze cycle, so the listing is marked suspended, and all market reads
-- (payments.getResaleStarGifts, UniqueStarGiftValueInfo, resell_amount in
-- uniqueStarGift, catalog projections) and purchases ignore it. Unfreezing returns
-- exactly the rows that were listed before the freeze back for sale at the same
-- price. The switch happens in the same transaction as the freeze itself
-- (SetAccountFreeze), so a window of "account frozen, listing still on sale" does
-- not exist.
ALTER TABLE public.star_gift_listings
    ADD COLUMN suspended boolean NOT NULL DEFAULT false;

-- The partial index serves only the freeze toggle: there are few frozen sellers but
-- many active listings.
CREATE INDEX star_gift_listings_seller_suspension_idx
    ON public.star_gift_listings(seller_peer_id)
    WHERE suspended AND seller_peer_type='user';

-- The same guard as in 0095, plus a ban on listing a gift of a frozen account.
-- The freeze itself performs the transition to suspended, so it is always allowed:
-- the frozen check concerns only active listings.
CREATE OR REPLACE FUNCTION public.telesrv_guard_star_gift_listing() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    gift_owner_type text;
    gift_owner_id bigint;
    gift_burned boolean;
    seller_frozen boolean;
BEGIN
    SELECT owner_peer_type, owner_peer_id, burned
      INTO gift_owner_type, gift_owner_id, gift_burned
      FROM public.unique_star_gifts WHERE id=NEW.unique_gift_id FOR SHARE;
    IF gift_burned OR gift_owner_type IS DISTINCT FROM NEW.seller_peer_type OR gift_owner_id IS DISTINCT FROM NEW.seller_peer_id THEN
        RAISE EXCEPTION 'star gift listing owner/state mismatch';
    END IF;
    IF NOT NEW.suspended AND NEW.seller_peer_type = 'user' THEN
        SELECT EXISTS(SELECT 1 FROM public.account_restrictions r WHERE r.user_id=NEW.seller_peer_id AND r.frozen)
          INTO seller_frozen;
        IF seller_frozen THEN
            RAISE EXCEPTION 'star gift listing seller frozen';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
