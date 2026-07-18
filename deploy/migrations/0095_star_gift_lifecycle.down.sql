DROP TRIGGER IF EXISTS peer_unique_star_gift_owner_guard ON public.peer_star_gifts;
DROP TRIGGER IF EXISTS unique_star_gift_owner_guard ON public.unique_star_gifts;
DROP FUNCTION IF EXISTS public.telesrv_check_unique_star_gift_owner();
DROP TRIGGER IF EXISTS star_gift_listing_guard ON public.star_gift_listings;
DROP FUNCTION IF EXISTS public.telesrv_guard_star_gift_listing();

DROP TABLE IF EXISTS public.ton_transactions;
DROP TABLE IF EXISTS public.ton_balances;
DROP TABLE IF EXISTS public.star_gift_auction_acquired;
DROP TABLE IF EXISTS public.star_gift_auction_bid_payments;
DROP TABLE IF EXISTS public.star_gift_auction_bids;
DROP TABLE IF EXISTS public.star_gift_auctions;
DROP TABLE IF EXISTS public.star_gift_withdrawal_requests;
DROP TABLE IF EXISTS public.star_gift_notification_settings;
DROP TABLE IF EXISTS public.star_gift_craft_commands;
DROP TABLE IF EXISTS public.star_gift_transfer_commands;
DROP TABLE IF EXISTS public.star_gift_purchase_commands;
DROP TABLE IF EXISTS public.star_gift_drop_details_commands;
DROP TABLE IF EXISTS public.star_gift_prepaid_upgrade_commands;
DROP TABLE IF EXISTS public.star_gift_offers;
DROP TABLE IF EXISTS public.star_gift_sales;
DROP TABLE IF EXISTS public.star_gift_listings;

ALTER TABLE public.unique_star_gifts
    DROP CONSTRAINT IF EXISTS unique_star_gift_value_check,
	DROP CONSTRAINT IF EXISTS unique_star_gift_original_owner_check,
    DROP CONSTRAINT IF EXISTS unique_star_gift_host_peer_check,
    DROP CONSTRAINT IF EXISTS unique_star_gift_theme_peer_check,
    DROP CONSTRAINT IF EXISTS unique_star_gift_released_by_check,
    DROP CONSTRAINT IF EXISTS unique_star_gift_owner_check,
    DROP COLUMN IF EXISTS last_sale_amount,
    DROP COLUMN IF EXISTS last_sale_currency,
    DROP COLUMN IF EXISTS last_sale_date,
    DROP COLUMN IF EXISTS craft_chance_permille,
    DROP COLUMN IF EXISTS offer_min_stars,
    DROP COLUMN IF EXISTS host_peer_id,
    DROP COLUMN IF EXISTS host_peer_type,
    DROP COLUMN IF EXISTS theme_peer_id,
    DROP COLUMN IF EXISTS theme_peer_type,
    DROP COLUMN IF EXISTS value_usd_amount,
    DROP COLUMN IF EXISTS value_currency,
    DROP COLUMN IF EXISTS value_amount,
    DROP COLUMN IF EXISTS released_by_peer_id,
    DROP COLUMN IF EXISTS released_by_peer_type,
    DROP COLUMN IF EXISTS gift_address,
    DROP COLUMN IF EXISTS owner_address,
    DROP COLUMN IF EXISTS owner_name,
    DROP COLUMN IF EXISTS crafted,
	DROP COLUMN IF EXISTS original_owner_peer_id,
	DROP COLUMN IF EXISTS original_owner_peer_type,
    DROP COLUMN IF EXISTS burned,
    DROP COLUMN IF EXISTS theme_available,
    DROP COLUMN IF EXISTS resale_ton_only,
    DROP COLUMN IF EXISTS require_premium;

UPDATE public.unique_star_gifts u
SET owner_peer_type=p.owner_peer_type, owner_peer_id=p.owner_peer_id
FROM public.peer_star_gifts p
WHERE p.unique_gift_id=u.id AND (u.owner_peer_type IS NULL OR u.owner_peer_id IS NULL);
ALTER TABLE public.unique_star_gifts
    ALTER COLUMN owner_peer_type SET NOT NULL,
    ALTER COLUMN owner_peer_id SET NOT NULL,
    ADD CONSTRAINT unique_star_gift_owner_check CHECK (owner_peer_type IN ('user','channel') AND owner_peer_id>0);

ALTER TABLE public.peer_star_gifts
	DROP COLUMN IF EXISTS prepaid_upgrade_hash,
	DROP COLUMN IF EXISTS gift_num,
    DROP CONSTRAINT IF EXISTS peer_star_gifts_lifecycle_check,
    DROP COLUMN IF EXISTS can_craft_at,
    DROP COLUMN IF EXISTS drop_original_details_stars,
    DROP COLUMN IF EXISTS can_resell_at,
    DROP COLUMN IF EXISTS can_transfer_at,
    DROP COLUMN IF EXISTS can_export_at,
    DROP COLUMN IF EXISTS transfer_stars,
    DROP COLUMN IF EXISTS lifecycle_status,
    ADD CONSTRAINT peer_star_gifts_terminal_state_check CHECK (NOT converted OR unique_gift_id IS NULL);
