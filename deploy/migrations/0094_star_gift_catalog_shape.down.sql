DROP TABLE IF EXISTS public.star_gift_user_purchases;

ALTER TABLE public.star_gift_catalog_revisions
    DROP CONSTRAINT IF EXISTS star_gift_catalog_revision_background_check,
    DROP CONSTRAINT IF EXISTS star_gift_catalog_revision_auction_check,
    DROP CONSTRAINT IF EXISTS star_gift_catalog_revision_released_by_check,
    DROP CONSTRAINT IF EXISTS star_gift_catalog_revision_supply_check,
    DROP COLUMN IF EXISTS background_text_color,
    DROP COLUMN IF EXISTS background_edge_color,
    DROP COLUMN IF EXISTS background_center_color,
    DROP COLUMN IF EXISTS upgrade_variants,
    DROP COLUMN IF EXISTS auction_start_date,
    DROP COLUMN IF EXISTS gifts_per_round,
    DROP COLUMN IF EXISTS auction_slug,
    DROP COLUMN IF EXISTS locked_until_date,
    DROP COLUMN IF EXISTS per_user_total,
    DROP COLUMN IF EXISTS released_by_peer_id,
    DROP COLUMN IF EXISTS released_by_peer_type,
    DROP COLUMN IF EXISTS availability_total,
    DROP COLUMN IF EXISTS auction,
    DROP COLUMN IF EXISTS peer_color_available,
    DROP COLUMN IF EXISTS limited_per_user,
    DROP COLUMN IF EXISTS require_premium,
    DROP COLUMN IF EXISTS birthday,
    DROP COLUMN IF EXISTS sold_out,
    DROP COLUMN IF EXISTS limited;

ALTER TABLE public.star_gift_catalog
    DROP CONSTRAINT IF EXISTS star_gift_catalog_inventory_check,
    DROP COLUMN IF EXISTS last_sale_date,
    DROP COLUMN IF EXISTS first_sale_date,
    DROP COLUMN IF EXISTS availability_resale,
	DROP COLUMN IF EXISTS resell_min_stars,
    DROP COLUMN IF EXISTS availability_remains;
