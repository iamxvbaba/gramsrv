CREATE TABLE public.ephemeral_abuse_reports (
    id bigserial PRIMARY KEY,
    reporter_user_id bigint NOT NULL CHECK (reporter_user_id > 0),
    channel_id bigint NOT NULL CHECK (channel_id > 0),
    ephemeral_message_id integer NOT NULL CHECK (ephemeral_message_id > 0),
    sender_user_id bigint NOT NULL CHECK (sender_user_id > 0),
    receiver_user_id bigint NOT NULL CHECK (receiver_user_id = reporter_user_id),
    report_option text NOT NULL CHECK (length(report_option) BETWEEN 1 AND 64),
    report_comment text NOT NULL DEFAULT '' CHECK (length(report_comment) <= 4096),
    comment_hash bytea NOT NULL CHECK (octet_length(comment_hash) = 32),
    payload_hash bytea NOT NULL CHECK (octet_length(payload_hash) = 32),
    evidence jsonb NOT NULL CHECK (jsonb_typeof(evidence) = 'object'),
    created_at timestamptz NOT NULL,
    CONSTRAINT ephemeral_abuse_reports_idempotency UNIQUE (
        reporter_user_id, channel_id, ephemeral_message_id, report_option, comment_hash
    )
);

CREATE INDEX ephemeral_abuse_reports_created_at_idx
    ON public.ephemeral_abuse_reports (created_at DESC, id DESC);

CREATE INDEX ephemeral_abuse_reports_sender_created_idx
    ON public.ephemeral_abuse_reports (sender_user_id, created_at DESC, id DESC);
