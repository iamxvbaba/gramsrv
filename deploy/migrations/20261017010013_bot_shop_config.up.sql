-- Price list of the numbers bot shop, edited in the admin panel.
CREATE TABLE public.bot_shop_config (
    id smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    config jsonb NOT NULL,
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);
