-- Возвращает guard из 0095: без колонки suspended он больше не нужен.
CREATE OR REPLACE FUNCTION public.telesrv_guard_star_gift_listing() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    gift_owner_type text;
    gift_owner_id bigint;
    gift_burned boolean;
BEGIN
    SELECT owner_peer_type, owner_peer_id, burned
      INTO gift_owner_type, gift_owner_id, gift_burned
      FROM public.unique_star_gifts WHERE id=NEW.unique_gift_id FOR SHARE;
    IF gift_burned OR gift_owner_type IS DISTINCT FROM NEW.seller_peer_type OR gift_owner_id IS DISTINCT FROM NEW.seller_peer_id THEN
        RAISE EXCEPTION 'star gift listing owner/state mismatch';
    END IF;
    RETURN NEW;
END;
$$;

DROP INDEX IF EXISTS public.star_gift_listings_seller_suspension_idx;

ALTER TABLE public.star_gift_listings
    DROP COLUMN IF EXISTS suspended;
