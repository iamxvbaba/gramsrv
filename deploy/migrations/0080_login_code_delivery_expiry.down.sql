DROP INDEX IF EXISTS login_code_message_deliveries_expiry_idx;

ALTER TABLE login_code_message_deliveries
    DROP COLUMN IF EXISTS expires_at;
