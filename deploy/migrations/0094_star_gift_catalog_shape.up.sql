-- Preserve the complete Layer 228 regular StarGift shape. Release facts are immutable
-- catalog-revision data; inventory and sale timestamps belong to the mutable catalog
-- aggregate. Per-user ownership limits are enforced by the transaction boundary rather
-- than reconstructed from peer_star_gifts after the fact.

ALTER TABLE public.star_gift_catalog
    ADD COLUMN availability_remains integer DEFAULT 0 NOT NULL,
    ADD COLUMN availability_resale bigint DEFAULT 0 NOT NULL,
	ADD COLUMN resell_min_stars bigint DEFAULT 0 NOT NULL,
    ADD COLUMN first_sale_date integer DEFAULT 0 NOT NULL,
    ADD COLUMN last_sale_date integer DEFAULT 0 NOT NULL,
    ADD CONSTRAINT star_gift_catalog_inventory_check CHECK (
		availability_remains >= 0 AND availability_resale >= 0 AND resell_min_stars >= 0 AND
        first_sale_date >= 0 AND last_sale_date >= 0 AND
        (last_sale_date = 0 OR first_sale_date > 0) AND
        (first_sale_date = 0 OR last_sale_date = 0 OR last_sale_date >= first_sale_date)
    );

ALTER TABLE public.star_gift_catalog_revisions
    ADD COLUMN limited boolean DEFAULT false NOT NULL,
    ADD COLUMN sold_out boolean DEFAULT false NOT NULL,
    ADD COLUMN birthday boolean DEFAULT false NOT NULL,
    ADD COLUMN require_premium boolean DEFAULT false NOT NULL,
    ADD COLUMN limited_per_user boolean DEFAULT false NOT NULL,
    ADD COLUMN peer_color_available boolean DEFAULT false NOT NULL,
    ADD COLUMN auction boolean DEFAULT false NOT NULL,
    ADD COLUMN availability_total integer DEFAULT 0 NOT NULL,
    ADD COLUMN released_by_peer_type text,
    ADD COLUMN released_by_peer_id bigint,
    ADD COLUMN per_user_total integer DEFAULT 0 NOT NULL,
    ADD COLUMN locked_until_date integer DEFAULT 0 NOT NULL,
    ADD COLUMN auction_slug text DEFAULT '' NOT NULL,
    ADD COLUMN gifts_per_round integer DEFAULT 0 NOT NULL,
    ADD COLUMN auction_start_date integer DEFAULT 0 NOT NULL,
    ADD COLUMN upgrade_variants integer DEFAULT 0 NOT NULL,
    ADD COLUMN background_center_color integer,
    ADD COLUMN background_edge_color integer,
    ADD COLUMN background_text_color integer,
    ADD CONSTRAINT star_gift_catalog_revision_supply_check CHECK (
		availability_total >= 0 AND
        per_user_total >= 0 AND locked_until_date >= 0 AND upgrade_variants >= 0 AND
        ((limited AND availability_total > 0) OR (NOT limited AND availability_total = 0)) AND
        (NOT sold_out OR limited) AND
        ((limited_per_user AND per_user_total > 0) OR (NOT limited_per_user AND per_user_total = 0))
    ),
    ADD CONSTRAINT star_gift_catalog_revision_released_by_check CHECK (
        (released_by_peer_type IS NULL AND released_by_peer_id IS NULL) OR
        (released_by_peer_type IN ('user', 'chat', 'channel') AND released_by_peer_id > 0)
    ),
    ADD CONSTRAINT star_gift_catalog_revision_auction_check CHECK (
        (auction AND limited AND auction_slug <> '' AND gifts_per_round > 0 AND auction_start_date > 0) OR
        (NOT auction AND auction_slug = '' AND gifts_per_round = 0 AND auction_start_date = 0)
    ),
    ADD CONSTRAINT star_gift_catalog_revision_background_check CHECK (
        (background_center_color IS NULL AND background_edge_color IS NULL AND background_text_color IS NULL) OR
        (background_center_color BETWEEN 0 AND 16777215 AND
         background_edge_color BETWEEN 0 AND 16777215 AND
         background_text_color BETWEEN 0 AND 16777215)
    );

CREATE TABLE public.star_gift_user_purchases (
    user_id bigint NOT NULL,
    gift_id bigint NOT NULL REFERENCES public.star_gift_catalog(gift_id) ON DELETE RESTRICT,
    purchased_count integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT star_gift_user_purchases_pkey PRIMARY KEY (user_id, gift_id),
    CONSTRAINT star_gift_user_purchases_count_check CHECK (user_id > 0 AND purchased_count >= 0)
);
