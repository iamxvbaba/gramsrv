CREATE TABLE public.bot_widgets (
    widget_id BIGINT PRIMARY KEY,
    bot_user_id BIGINT NOT NULL,
    html TEXT NOT NULL DEFAULT '',
    payload BYTEA NOT NULL DEFAULT '\x',
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    allow_queries BOOLEAN NOT NULL DEFAULT FALSE,
    public_data TEXT NOT NULL DEFAULT '',
    public_data_hash BIGINT NOT NULL DEFAULT 0,
    cache_time INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX bot_widgets_bot_idx ON public.bot_widgets (bot_user_id);
