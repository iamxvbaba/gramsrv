-- Shop-wide settings shared with the FlashGram bot.
-- stars_rate: FG Stars credited per one Telegram Star (the bot's purchase rate).

CREATE TABLE public.shop_settings (
    key text PRIMARY KEY,
    value bigint NOT NULL,
    updated_by text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

-- Seed with the rate currently configured on the bot (settings.json), so the
-- first admin-panel read shows the live value instead of an empty field.
INSERT INTO public.shop_settings (key, value, updated_by)
VALUES ('stars_rate', 100, 'migration')
ON CONFLICT (key) DO NOTHING;
