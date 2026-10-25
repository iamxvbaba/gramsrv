-- Restores the v2 0186 guard (owner/state plus externalization_pending).
CREATE OR REPLACE FUNCTION public.telesrv_guard_star_gift_listing() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    gift_owner_type text;
    gift_owner_id bigint;
    gift_burned boolean;
    gift_externalizing boolean;
BEGIN
    SELECT owner_peer_type, owner_peer_id, burned, externalization_pending
      INTO gift_owner_type, gift_owner_id, gift_burned, gift_externalizing
      FROM public.unique_star_gifts WHERE id=NEW.unique_gift_id FOR SHARE;
    IF gift_burned OR gift_externalizing OR
       gift_owner_type IS DISTINCT FROM NEW.seller_peer_type OR gift_owner_id IS DISTINCT FROM NEW.seller_peer_id THEN
        RAISE EXCEPTION 'star gift listing owner/state mismatch';
    END IF;
    RETURN NEW;
END;
$$;

DROP INDEX IF EXISTS public.star_gift_listings_seller_suspension_idx;

ALTER TABLE public.star_gift_listings
    DROP COLUMN IF EXISTS suspended;
