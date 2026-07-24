-- Authentication-code delivery diagnostics have a separate privacy and
-- retention boundary from abuse moderation. Raw phone numbers, raw
-- phone_code_hash values and authentication codes are never stored here.
CREATE TABLE public.auth_delivery_reports (
    id bigserial PRIMARY KEY,
    auth_key_id bytea NOT NULL CHECK (octet_length(auth_key_id) = 8),
    session_id bigint NOT NULL CHECK (session_id <> 0),
    client_type text NOT NULL CHECK (octet_length(client_type) <= 32),
    phone_hash bytea NOT NULL CHECK (octet_length(phone_hash) = 32),
    code_hash bytea NOT NULL CHECK (octet_length(code_hash) = 32),
    issued_user_id bigint NOT NULL CHECK (issued_user_id >= 0),
    delivery_id text NOT NULL CHECK (octet_length(delivery_id) <= 128),
    channel text NOT NULL CHECK (channel IN ('phone', 'sms')),
    mnc text NOT NULL CHECK (
        octet_length(mnc) <= 8 AND mnc !~ '[^0-9]'
    ),
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    created_at timestamptz NOT NULL,
    CONSTRAINT auth_delivery_reports_idempotency
        UNIQUE (auth_key_id, fingerprint)
);

CREATE INDEX auth_delivery_reports_auth_key_created_idx
    ON public.auth_delivery_reports (auth_key_id, created_at DESC, id DESC);

CREATE INDEX auth_delivery_reports_phone_created_idx
    ON public.auth_delivery_reports (phone_hash, created_at DESC, id DESC);
