-- v2 编号 0240；移植自 main 0211_pending_password_change（main e5971805），见 docs/main-v2-sync.md。
ALTER TABLE account_passwords
  ADD COLUMN pending_reset_date integer NOT NULL DEFAULT 0 CHECK (pending_reset_date >= 0),
  ADD COLUMN pending_change jsonb CHECK (pending_change IS NULL OR octet_length(pending_change::text) <= 16384);
