CREATE TABLE public.bot_api_webhooks (
    bot_user_id bigint PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    url text NOT NULL,
    secret_token varchar(256) NOT NULL DEFAULT '',
    max_connections integer NOT NULL DEFAULT 40,
    allowed_updates text[],
    failure_count integer NOT NULL DEFAULT 0,
    last_error_date integer NOT NULL DEFAULT 0,
    last_error_message varchar(512) NOT NULL DEFAULT '',
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    delivery_owner text NOT NULL DEFAULT '',
    delivery_expires_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT bot_api_webhooks_url_check CHECK (length(url) BETWEEN 1 AND 2048),
    CONSTRAINT bot_api_webhooks_max_connections_check CHECK (max_connections BETWEEN 1 AND 100),
    CONSTRAINT bot_api_webhooks_failure_count_check CHECK (failure_count >= 0),
    CONSTRAINT bot_api_webhooks_allowed_updates_check CHECK (
        allowed_updates IS NULL OR array_position(allowed_updates, NULL) IS NULL
    ),
    CONSTRAINT bot_api_webhooks_delivery_lease_check CHECK (
        (delivery_owner = '' AND delivery_expires_at IS NULL)
        OR (delivery_owner <> '' AND delivery_expires_at IS NOT NULL)
    )
);

CREATE INDEX bot_api_webhooks_due_idx
    ON public.bot_api_webhooks (next_attempt_at, bot_user_id);
