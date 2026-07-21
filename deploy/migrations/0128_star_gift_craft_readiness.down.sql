-- Aggregate/message repairs and emitted edit events are authoritative business
-- history and are intentionally not reversed. Restore only the pre-0128
-- deferred owner guard shape.
CREATE OR REPLACE FUNCTION public.telesrv_check_unique_star_gift_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    unique_id bigint;
    gift_owner_type text;
    gift_owner_id bigint;
    gift_owner_address text;
    gift_burned boolean;
    saved_status text;
    saved_owner_type text;
    saved_owner_id bigint;
BEGIN
    IF TG_TABLE_NAME = 'unique_star_gifts' THEN
        unique_id := COALESCE(NEW.id, OLD.id);
    ELSE
        unique_id := COALESCE(NEW.unique_gift_id, OLD.unique_gift_id);
    END IF;
    IF unique_id IS NULL THEN RETURN NULL; END IF;
    SELECT owner_peer_type, owner_peer_id, owner_address, burned
      INTO gift_owner_type, gift_owner_id, gift_owner_address, gift_burned
      FROM public.unique_star_gifts WHERE id=unique_id;
    IF NOT FOUND THEN RETURN NULL; END IF;
    SELECT lifecycle_status, owner_peer_type, owner_peer_id
      INTO saved_status, saved_owner_type, saved_owner_id
      FROM public.peer_star_gifts WHERE unique_gift_id=unique_id;
    IF NOT FOUND THEN RAISE EXCEPTION 'unique star gift missing saved aggregate'; END IF;
    IF gift_burned THEN
        IF saved_status <> 'burned' THEN RAISE EXCEPTION 'burned unique star gift has live saved aggregate'; END IF;
    ELSIF gift_owner_address <> '' THEN
        IF saved_status <> 'exported' THEN RAISE EXCEPTION 'exported unique star gift has non-exported saved aggregate'; END IF;
    ELSIF saved_status <> 'active' OR gift_owner_type IS DISTINCT FROM saved_owner_type OR gift_owner_id IS DISTINCT FROM saved_owner_id THEN
        RAISE EXCEPTION 'unique star gift owner mismatch';
    END IF;
    RETURN NULL;
END;
$$;
