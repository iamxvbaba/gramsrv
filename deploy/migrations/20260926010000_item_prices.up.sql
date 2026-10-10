-- Per-item price overrides for admin-configurable catalog pricing.
-- Falls back to catalog defaults when no override exists.

CREATE TABLE public.item_prices (
    product_code text PRIMARY KEY,        -- e.g., 'premium_1m', 'uname_10', 'num_short'
    stars_price integer NOT NULL,         -- price in Telegram Stars
    bid bigint DEFAULT 0 NOT NULL,        -- for username/number products (TON bid)
    enabled boolean DEFAULT true NOT NULL,
    updated_by text NOT NULL,             -- admin user who last changed it
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE INDEX item_prices_enabled_idx ON public.item_prices(enabled) WHERE enabled;

-- Seed with current catalog defaults (will be used as fallbacks if table empty)
-- Actual seeding happens in code on first run or via admin UI.
