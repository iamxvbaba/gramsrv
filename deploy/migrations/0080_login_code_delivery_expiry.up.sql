-- Compact login-code idempotency receipts are only needed while the opaque
-- code can still be used/replayed. Keep them seek-prunable instead of growing
-- one row per login attempt forever.
ALTER TABLE login_code_message_deliveries
    ADD COLUMN expires_at timestamptz;

UPDATE login_code_message_deliveries
SET expires_at = created_at + interval '24 hours'
WHERE expires_at IS NULL;

ALTER TABLE login_code_message_deliveries
    ALTER COLUMN expires_at SET NOT NULL;

CREATE INDEX login_code_message_deliveries_expiry_idx
    ON login_code_message_deliveries (expires_at, delivery_key);
