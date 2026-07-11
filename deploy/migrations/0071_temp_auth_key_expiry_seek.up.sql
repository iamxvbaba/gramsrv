-- DeleteExpiredTempAuthKeys orders/filters the binding table by expiry and then deletes the parent
-- auth_keys rows in a bounded batch.  The PK starts with temp_auth_key_id, so it cannot serve this
-- maintenance seek.
CREATE INDEX IF NOT EXISTS temp_auth_key_bindings_expiry_idx
    ON public.temp_auth_key_bindings (expires_at, temp_auth_key_id);
