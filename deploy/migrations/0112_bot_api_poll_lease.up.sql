ALTER TABLE public.bot_api_update_states
    ADD COLUMN poll_owner text NOT NULL DEFAULT '',
    ADD COLUMN poll_expires_at timestamptz;

ALTER TABLE public.bot_api_update_states
    ADD CONSTRAINT bot_api_update_states_poll_lease_check CHECK (
        (poll_owner = '' AND poll_expires_at IS NULL)
        OR
        (poll_owner <> '' AND poll_expires_at IS NOT NULL)
    );
