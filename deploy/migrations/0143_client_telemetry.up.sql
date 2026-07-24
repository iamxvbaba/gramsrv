-- Operational client telemetry is not an abuse-report source. It has its own
-- idempotency/rate-limit indexes and TTL retention boundary.
CREATE TABLE public.client_telemetry_events (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL CHECK (user_id > 0),
    kind text NOT NULL CHECK (
        kind IN ('message_delivery', 'read_metrics', 'music_listen')
    ),
    peer_type text NOT NULL CHECK (peer_type IN ('', 'user', 'channel')),
    peer_id bigint NOT NULL CHECK (
        (peer_type = '' AND peer_id = 0)
        OR (peer_type <> '' AND peer_id > 0)
    ),
    subject_ids bigint[] NOT NULL CHECK (
        cardinality(subject_ids) BETWEEN 1 AND 100
    ),
    payload jsonb NOT NULL CHECK (
        jsonb_typeof(payload) = 'object'
        AND octet_length(payload::text) <= 65536
    ),
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    created_at timestamptz NOT NULL,
    CONSTRAINT client_telemetry_idempotency UNIQUE (user_id, fingerprint)
);

CREATE INDEX client_telemetry_user_created_idx
    ON public.client_telemetry_events (user_id, created_at DESC, id DESC);

CREATE INDEX client_telemetry_retention_idx
    ON public.client_telemetry_events (created_at, id);
