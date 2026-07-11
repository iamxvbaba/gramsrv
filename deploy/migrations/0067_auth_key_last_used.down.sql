DROP INDEX IF EXISTS public.temp_auth_key_bindings_perm_idx;
DROP INDEX IF EXISTS public.auth_keys_orphan_last_used_idx;
ALTER TABLE public.auth_keys DROP COLUMN IF EXISTS last_used_at;

-- Restore the 0065 schema used by the pre-last_used orphan collector.
CREATE INDEX IF NOT EXISTS auth_keys_orphan_retention_idx
    ON public.auth_keys (created_at, auth_key_id);
