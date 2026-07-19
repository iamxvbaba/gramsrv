DROP INDEX IF EXISTS public.bot_api_updates_created_retention_idx;

ALTER TABLE public.bot_api_update_states
    DROP CONSTRAINT IF EXISTS bot_api_update_states_allowed_updates_check,
    DROP COLUMN IF EXISTS cursor_initialized,
    DROP COLUMN IF EXISTS allowed_updates;
