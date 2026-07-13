ALTER TABLE public.temp_auth_key_bindings
    DROP CONSTRAINT IF EXISTS temp_auth_key_bindings_perm_auth_key_id_fkey;

DROP INDEX IF EXISTS public.auth_keys_temporary_expiry_seek_idx;

ALTER TABLE public.auth_keys
    DROP CONSTRAINT IF EXISTS auth_keys_expires_at_valid;

ALTER TABLE public.auth_keys
    DROP COLUMN IF EXISTS expires_at;
