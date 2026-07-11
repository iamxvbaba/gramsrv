-- A phone_code_hash identifies exactly one account-visible 777000 login-code
-- notification. Store only its SHA-256 digest plus compact immutable allocation
-- facts; the secret code body remains solely in private_messages/message_boxes.
CREATE TABLE login_code_message_deliveries (
    delivery_key bytea PRIMARY KEY,
    code_fingerprint bytea NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    private_message_id bigint NOT NULL CHECK (private_message_id > 0),
    message_box_id integer NOT NULL CHECK (message_box_id > 0),
    pts integer NOT NULL CHECK (pts > 0),
    message_date integer NOT NULL CHECK (message_date > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT login_code_message_deliveries_key_size CHECK (octet_length(delivery_key) = 32),
    CONSTRAINT login_code_message_deliveries_fingerprint_size CHECK (octet_length(code_fingerprint) = 32),
    CONSTRAINT login_code_message_deliveries_user_box_unique UNIQUE (user_id, message_box_id),
    CONSTRAINT login_code_message_deliveries_user_pts_unique UNIQUE (user_id, pts)
);

COMMENT ON COLUMN login_code_message_deliveries.delivery_key IS
    'SHA-256(phone_code_hash); raw phone_code_hash is never persisted';
COMMENT ON COLUMN login_code_message_deliveries.code_fingerprint IS
    'HMAC-SHA-256(code), keyed by the non-persisted raw phone_code_hash';
