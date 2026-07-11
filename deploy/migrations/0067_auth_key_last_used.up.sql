-- A physical connection touches last_used_at atomically while loading its key.  Orphan GC uses
-- this watermark in addition to the in-memory active-key snapshot, closing the Get->Register
-- race without turning the per-frame encrypted fast path into a database write.
ALTER TABLE public.auth_keys
    ADD COLUMN IF NOT EXISTS last_used_at timestamptz NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS auth_keys_orphan_last_used_idx
    ON public.auth_keys (last_used_at, auth_key_id);

-- DeleteOrphaned now seeks exclusively by last_used_at. Keeping the transitional 0065
-- created_at index would duplicate auth-key insert/delete maintenance without serving a query.
DROP INDEX IF EXISTS public.auth_keys_orphan_retention_idx;

-- Delete/revoke and orphan-retention probes both resolve perm->temp bindings.  The original
-- primary key only covers temp_auth_key_id, so the reverse predicate otherwise scans the table.
CREATE INDEX IF NOT EXISTS temp_auth_key_bindings_perm_idx
    ON public.temp_auth_key_bindings (perm_auth_key_id);
