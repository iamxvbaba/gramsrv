-- Server-issued evidence registries. These prevent arbitrary sponsored IDs or
-- ordinary deleted messages from being accepted as human reports.
CREATE TABLE public.sponsored_message_impressions (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL CHECK (user_id > 0),
    random_id_hash bytea NOT NULL CHECK (octet_length(random_id_hash) = 32),
    target_peer_type text NOT NULL CHECK (target_peer_type IN ('user', 'channel')),
    target_peer_id bigint NOT NULL CHECK (target_peer_id > 0),
    author_user_id bigint NOT NULL CHECK (author_user_id >= 0),
    evidence_schema_version smallint NOT NULL CHECK (evidence_schema_version > 0),
    evidence jsonb NOT NULL CHECK (
        jsonb_typeof(evidence) = 'object'
        AND octet_length(evidence::text) <= 1048576
    ),
    evidence_hash bytea NOT NULL CHECK (octet_length(evidence_hash) = 32),
    report_id bigint UNIQUE REFERENCES public.moderation_reports(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK (expires_at > created_at),
    CHECK (expires_at <= created_at + interval '30 days'),
    CONSTRAINT sponsored_message_impressions_identity
        UNIQUE (user_id, random_id_hash)
);

CREATE INDEX sponsored_message_impressions_expiry_idx
    ON public.sponsored_message_impressions (expires_at, id);

CREATE TABLE public.channel_antispam_decisions (
    id bigserial PRIMARY KEY,
    channel_id bigint NOT NULL CHECK (channel_id > 0),
    message_id integer NOT NULL CHECK (message_id > 0),
    author_user_id bigint NOT NULL CHECK (author_user_id > 0),
    evidence_schema_version smallint NOT NULL CHECK (evidence_schema_version > 0),
    evidence jsonb NOT NULL CHECK (
        jsonb_typeof(evidence) = 'object'
        AND octet_length(evidence::text) <= 1048576
    ),
    evidence_hash bytea NOT NULL CHECK (octet_length(evidence_hash) = 32),
    report_id bigint UNIQUE REFERENCES public.moderation_reports(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL,
    CONSTRAINT channel_antispam_decisions_identity
        UNIQUE (channel_id, message_id)
);

CREATE INDEX channel_antispam_decisions_unreported_idx
    ON public.channel_antispam_decisions (channel_id, created_at DESC, id DESC)
    WHERE report_id IS NULL;
