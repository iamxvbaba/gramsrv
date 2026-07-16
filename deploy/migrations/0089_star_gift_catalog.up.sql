-- Durable, administrator-managed regular Star Gift catalog. The previous seven-item
-- animated_emoji-derived directory was development-only and is intentionally not migrated.
-- Existing received rows refer to those non-durable definitions, so clear them instead of
-- manufacturing a read-time compatibility fallback that could no longer reconstruct assets.

CREATE SEQUENCE public.star_gift_catalog_gift_id_seq AS bigint START WITH 9000000000000001;
CREATE SEQUENCE public.star_gift_catalog_revision_id_seq AS bigint START WITH 1;

CREATE TABLE public.star_gift_catalog (
    gift_id bigint DEFAULT nextval('public.star_gift_catalog_gift_id_seq') NOT NULL,
    active_revision_id bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT star_gift_catalog_pkey PRIMARY KEY (gift_id)
);

CREATE TABLE public.star_gift_catalog_revisions (
    id bigint DEFAULT nextval('public.star_gift_catalog_revision_id_seq') NOT NULL,
    gift_id bigint NOT NULL,
    revision integer NOT NULL,
    title text DEFAULT '' NOT NULL,
    stars bigint NOT NULL,
    convert_stars bigint NOT NULL,
    document_id bigint NOT NULL,
    animation_json jsonb NOT NULL,
    animation_sha256 bytea NOT NULL,
    source_name text DEFAULT '' NOT NULL,
    source_format text NOT NULL,
    width integer NOT NULL,
    height integer NOT NULL,
    frame_rate double precision DEFAULT 0 NOT NULL,
    in_point double precision DEFAULT 0 NOT NULL,
    out_point double precision DEFAULT 0 NOT NULL,
    created_by text DEFAULT '' NOT NULL,
    command_id text DEFAULT '' NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT star_gift_catalog_revisions_pkey PRIMARY KEY (id),
    CONSTRAINT star_gift_catalog_revisions_gift_revision_uniq UNIQUE (gift_id, revision),
    CONSTRAINT star_gift_catalog_revisions_document_uniq UNIQUE (document_id),
    CONSTRAINT star_gift_catalog_revision_price_check CHECK (stars > 0 AND convert_stars >= 0 AND convert_stars <= stars),
    CONSTRAINT star_gift_catalog_revision_shape_check CHECK (width = 512 AND height = 512 AND jsonb_typeof(animation_json) = 'object'),
    CONSTRAINT star_gift_catalog_revision_source_check CHECK (source_format IN ('tgs', 'lottie')),
    CONSTRAINT star_gift_catalog_revision_gift_fk FOREIGN KEY (gift_id)
        REFERENCES public.star_gift_catalog(gift_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT star_gift_catalog_revision_document_fk FOREIGN KEY (document_id)
        REFERENCES public.documents(id) ON DELETE RESTRICT
);

ALTER TABLE public.star_gift_catalog
    ADD CONSTRAINT star_gift_catalog_active_revision_fk FOREIGN KEY (active_revision_id)
        REFERENCES public.star_gift_catalog_revisions(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX star_gift_catalog_enabled_order_idx
    ON public.star_gift_catalog (sort_order, gift_id) WHERE enabled;
CREATE INDEX star_gift_catalog_revisions_gift_idx
    ON public.star_gift_catalog_revisions (gift_id, revision DESC);

DELETE FROM public.peer_star_gifts;
ALTER TABLE public.peer_star_gifts
    ADD COLUMN catalog_revision_id bigint NOT NULL,
    ADD CONSTRAINT peer_star_gifts_catalog_revision_fk FOREIGN KEY (catalog_revision_id)
        REFERENCES public.star_gift_catalog_revisions(id) ON DELETE RESTRICT;
CREATE INDEX peer_star_gifts_catalog_revision_idx
    ON public.peer_star_gifts (catalog_revision_id);
CREATE INDEX peer_star_gifts_gift_idx
    ON public.peer_star_gifts (gift_id);

CREATE FUNCTION public.telesrv_notify_star_gift_catalog_changed() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM public.telesrv_bump_read_model_version('star_gift_catalog', 0, '', 0);
    RETURN NULL;
END;
$$;

CREATE TRIGGER star_gift_catalog_changed
    AFTER INSERT OR UPDATE OR DELETE ON public.star_gift_catalog
    FOR EACH STATEMENT EXECUTE FUNCTION public.telesrv_notify_star_gift_catalog_changed();
