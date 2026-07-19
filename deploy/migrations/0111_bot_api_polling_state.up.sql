ALTER TABLE public.bot_api_update_states
    ADD COLUMN allowed_updates text[],
    ADD COLUMN cursor_initialized boolean NOT NULL DEFAULT false;

ALTER TABLE public.bot_api_update_states
    ADD CONSTRAINT bot_api_update_states_allowed_updates_check CHECK (
        allowed_updates IS NULL
        OR array_position(allowed_updates, NULL) IS NULL
    );

CREATE INDEX bot_api_updates_created_retention_idx
    ON public.bot_api_updates (created_at, id);
