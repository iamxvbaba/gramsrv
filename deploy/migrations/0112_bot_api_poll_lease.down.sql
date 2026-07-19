ALTER TABLE public.bot_api_update_states
    DROP CONSTRAINT IF EXISTS bot_api_update_states_poll_lease_check,
    DROP COLUMN IF EXISTS poll_expires_at,
    DROP COLUMN IF EXISTS poll_owner;
