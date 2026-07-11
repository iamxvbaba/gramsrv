-- Bounded orphan auth-key GC seeks by creation time. Authorization and temp-key references are
-- rechecked in the DELETE statement; active raw keys are supplied by the connection registry.
CREATE INDEX IF NOT EXISTS auth_keys_orphan_retention_idx
    ON public.auth_keys (created_at, auth_key_id);
