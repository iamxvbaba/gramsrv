-- Admin "delete gift": the catalog row stays as the anchor every message,
-- purchase command and saved gift references, but is retired for good. A
-- deleted gift is never enabled again and is hidden from the admin list;
-- its ordinary saved copies move to the existing 'deleted' lifecycle.
ALTER TABLE public.star_gift_catalog
    ADD COLUMN deleted_at timestamp with time zone,
    ADD CONSTRAINT star_gift_catalog_deleted_disabled_check CHECK (deleted_at IS NULL OR NOT enabled);
