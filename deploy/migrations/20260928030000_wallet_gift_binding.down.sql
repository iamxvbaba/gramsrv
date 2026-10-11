ALTER TABLE public.unique_star_gifts
    DROP CONSTRAINT IF EXISTS unique_star_gift_value_check,
    ADD CONSTRAINT unique_star_gift_value_check CHECK (
        value_amount >= 0 AND value_usd_amount >= 0 AND offer_min_stars >= 0 AND
        craft_chance_permille BETWEEN 0 AND 1000 AND last_sale_date >= 0 AND last_sale_amount >= 0 AND
        ((value_currency='' AND value_amount=0) OR value_currency<>'') AND
        ((last_sale_currency='' AND last_sale_amount=0 AND last_sale_date=0) OR
         (last_sale_currency IN ('XTR','TON') AND last_sale_amount>0 AND last_sale_date>0)) AND
        (NOT burned OR owner_address='') AND
        ((owner_address='' AND gift_address='') OR (owner_address<>'' AND gift_address<>''))
    );

CREATE OR REPLACE FUNCTION public.telesrv_check_unique_star_gift_owner()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
DECLARE
    v_unique_gift_id bigint;
    gift_owner_type text;
    gift_owner_id bigint;
    gift_owner_address text;
    gift_burned boolean;
    gift_craft_chance integer;
    saved_status text;
    saved_owner_type text;
    saved_owner_id bigint;
    saved_can_craft_at integer;
BEGIN
    IF TG_TABLE_NAME = 'unique_star_gifts' THEN
        v_unique_gift_id := COALESCE(NEW.id, OLD.id);
    ELSE
        v_unique_gift_id := COALESCE(NEW.unique_gift_id, OLD.unique_gift_id);
    END IF;
    IF v_unique_gift_id IS NULL THEN
        RETURN NULL;
    END IF;

    SELECT usg.owner_peer_type, usg.owner_peer_id, usg.owner_address, usg.burned,
           usg.craft_chance_permille
      INTO gift_owner_type, gift_owner_id, gift_owner_address, gift_burned,
           gift_craft_chance
      FROM public.unique_star_gifts AS usg
     WHERE usg.id = v_unique_gift_id;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    SELECT psg.lifecycle_status, psg.owner_peer_type, psg.owner_peer_id,
           psg.can_craft_at
      INTO saved_status, saved_owner_type, saved_owner_id, saved_can_craft_at
      FROM public.peer_star_gifts AS psg
     WHERE psg.unique_gift_id = v_unique_gift_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'unique star gift missing saved aggregate';
    END IF;

    IF gift_burned THEN
        IF saved_status <> 'burned' THEN
            RAISE EXCEPTION 'burned unique star gift has live saved aggregate';
        END IF;
    ELSIF gift_owner_address <> '' THEN
        IF saved_status <> 'exported' THEN
            RAISE EXCEPTION 'exported unique star gift has non-exported saved aggregate';
        END IF;
    ELSIF saved_status <> 'active'
       OR gift_owner_type IS DISTINCT FROM saved_owner_type
       OR gift_owner_id IS DISTINCT FROM saved_owner_id THEN
        RAISE EXCEPTION 'unique star gift owner mismatch';
    END IF;

    -- Android treats positive can_craft_at as the capability marker, while
    -- TDesktop reads the unique gift chance. Keep both projections atomic.
    IF saved_status = 'active'
       AND ((gift_craft_chance > 0) IS DISTINCT FROM (saved_can_craft_at > 0)) THEN
        RAISE EXCEPTION 'unique star gift craft readiness mismatch';
    END IF;
    RETURN NULL;
END;
$function$
