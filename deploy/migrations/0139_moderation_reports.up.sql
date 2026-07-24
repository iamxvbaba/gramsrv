-- Unified immutable abuse-report submissions. Operational delivery/read/music
-- telemetry and auth-code delivery diagnostics intentionally use separate
-- tables and retention policies.
CREATE TABLE public.moderation_reports (
    id bigserial PRIMARY KEY,
    reporter_user_id bigint NOT NULL CHECK (reporter_user_id > 0),
    source text NOT NULL CHECK (source IN (
        'account_peer', 'profile_photo', 'messages_spam', 'messages',
        'encrypted_spam', 'reaction', 'channel_spam', 'story', 'ephemeral',
        'sponsored', 'antispam_false_positive'
    )),
    target_peer_type text NOT NULL CHECK (target_peer_type IN ('user', 'channel')),
    target_peer_id bigint NOT NULL CHECK (target_peer_id > 0),
    reason text NOT NULL CHECK (reason IN (
        'spam', 'violence', 'pornography', 'child_abuse', 'other',
        'copyright', 'geo_irrelevant', 'fake', 'illegal_drugs',
        'personal_details'
    )),
    report_option text NOT NULL CHECK (
        octet_length(report_option) BETWEEN 1 AND 32
    ),
    report_comment text NOT NULL DEFAULT '' CHECK (
        char_length(report_comment) <= 512
    ),
    comment_hash bytea NOT NULL CHECK (octet_length(comment_hash) = 32),
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    taxonomy_version smallint NOT NULL CHECK (taxonomy_version > 0),
    created_at timestamptz NOT NULL,
    CONSTRAINT moderation_reports_idempotency
        UNIQUE (reporter_user_id, fingerprint)
);

CREATE INDEX moderation_reports_target_created_idx
    ON public.moderation_reports (
        target_peer_type, target_peer_id, created_at DESC, id DESC
    );

CREATE INDEX moderation_reports_reporter_created_idx
    ON public.moderation_reports (
        reporter_user_id, created_at DESC, id DESC
    );

CREATE TABLE public.moderation_report_items (
    report_id bigint NOT NULL REFERENCES public.moderation_reports(id)
        ON DELETE CASCADE,
    ordinal smallint NOT NULL CHECK (ordinal BETWEEN 0 AND 99),
    item_kind text NOT NULL CHECK (item_kind IN (
        'peer', 'message', 'profile_photo', 'reaction', 'story',
        'encrypted_chat', 'ephemeral', 'sponsored', 'antispam_decision'
    )),
    peer_type text NOT NULL CHECK (peer_type IN ('user', 'channel')),
    peer_id bigint NOT NULL CHECK (peer_id > 0),
    item_id bigint NOT NULL CHECK (item_id > 0),
    secondary_id bigint NOT NULL DEFAULT 0 CHECK (secondary_id >= 0),
    author_user_id bigint NOT NULL DEFAULT 0 CHECK (author_user_id >= 0),
    evidence_schema_version smallint NOT NULL CHECK (
        evidence_schema_version > 0
    ),
    evidence jsonb NOT NULL CHECK (
        jsonb_typeof(evidence) = 'object'
        AND octet_length(evidence::text) <= 1048576
    ),
    evidence_hash bytea NOT NULL CHECK (octet_length(evidence_hash) = 32),
    PRIMARY KEY (report_id, ordinal),
    CONSTRAINT moderation_report_items_identity
        UNIQUE (
            report_id, item_kind, peer_type, peer_id, item_id, secondary_id
        )
);

CREATE INDEX moderation_report_items_lookup_idx
    ON public.moderation_report_items (
        item_kind, peer_type, peer_id, item_id, report_id
    );

CREATE INDEX moderation_report_items_author_idx
    ON public.moderation_report_items (
        author_user_id, report_id
    )
    WHERE author_user_id > 0;

CREATE TABLE public.moderation_media_holds (
    report_id bigint NOT NULL,
    item_ordinal smallint NOT NULL,
    media_kind text NOT NULL CHECK (media_kind IN ('photo', 'document', 'blob')),
    storage_key text NOT NULL CHECK (
        octet_length(storage_key) BETWEEN 1 AND 512
    ),
    created_at timestamptz NOT NULL,
    released_at timestamptz,
    PRIMARY KEY (report_id, item_ordinal, media_kind, storage_key),
    FOREIGN KEY (report_id, item_ordinal)
        REFERENCES public.moderation_report_items(report_id, ordinal)
        ON DELETE CASCADE,
    CHECK (released_at IS NULL OR released_at >= created_at)
);

CREATE INDEX moderation_media_holds_active_key_idx
    ON public.moderation_media_holds (media_kind, storage_key, report_id)
    WHERE released_at IS NULL;

-- Crash-safe, one-way provenance for rows written by the pre-unified
-- ephemeral.reportMessage implementation. The legacy table remains immutable
-- until every deployed database has completed the application-level evidence
-- conversion; all new writes go exclusively to moderation_reports.
CREATE TABLE public.moderation_legacy_ephemeral_migrations (
    legacy_report_id bigint PRIMARY KEY
        REFERENCES public.ephemeral_abuse_reports(id) ON DELETE RESTRICT,
    moderation_report_id bigint NOT NULL
        REFERENCES public.moderation_reports(id) ON DELETE RESTRICT,
    migrated_at timestamptz NOT NULL
);
