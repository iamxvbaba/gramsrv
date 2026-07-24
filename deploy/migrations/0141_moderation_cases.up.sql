-- Target-grouped moderation work queue. Reports stay immutable; cases,
-- decisions, actions and appeals form a separate optimistic-concurrency state
-- machine.
CREATE TABLE public.moderation_cases (
    id bigserial PRIMARY KEY,
    target_peer_type text NOT NULL CHECK (target_peer_type IN ('user', 'channel')),
    target_peer_id bigint NOT NULL CHECK (target_peer_id > 0),
    status text NOT NULL CHECK (status IN (
        'open', 'in_review', 'action_pending', 'action_failed', 'resolved',
        'dismissed', 'appeal_review'
    )),
    severity smallint NOT NULL CHECK (severity BETWEEN 1 AND 4),
    assigned_to text NOT NULL DEFAULT '' CHECK (octet_length(assigned_to) <= 128),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    report_count integer NOT NULL CHECK (report_count > 0),
    distinct_reporter_count integer NOT NULL CHECK (
        distinct_reporter_count > 0
        AND distinct_reporter_count <= report_count
    ),
    first_report_at timestamptz NOT NULL,
    last_report_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (last_report_at >= first_report_at),
    CHECK (updated_at >= created_at)
);

CREATE UNIQUE INDEX moderation_cases_one_active_target_idx
    ON public.moderation_cases (target_peer_type, target_peer_id)
    WHERE status IN ('open', 'in_review');

CREATE INDEX moderation_cases_queue_idx
    ON public.moderation_cases (status, severity DESC, updated_at DESC, id DESC);

CREATE INDEX moderation_cases_assignee_idx
    ON public.moderation_cases (assigned_to, status, updated_at DESC, id DESC)
    WHERE assigned_to <> '';

CREATE TABLE public.moderation_case_reports (
    case_id bigint NOT NULL REFERENCES public.moderation_cases(id)
        ON DELETE RESTRICT,
    report_id bigint NOT NULL UNIQUE REFERENCES public.moderation_reports(id)
        ON DELETE RESTRICT,
    attached_at timestamptz NOT NULL,
    PRIMARY KEY (case_id, report_id)
);

CREATE INDEX moderation_case_reports_case_idx
    ON public.moderation_case_reports (case_id, report_id);

CREATE TABLE public.moderation_decisions (
    id bigserial PRIMARY KEY,
    case_id bigint NOT NULL REFERENCES public.moderation_cases(id)
        ON DELETE RESTRICT,
    appeal_id bigint,
    kind text NOT NULL CHECK (kind IN (
        'no_violation', 'violation', 'appeal_granted', 'appeal_denied'
    )),
    actor text NOT NULL CHECK (octet_length(actor) BETWEEN 1 AND 128),
    reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 2000),
    command_id text NOT NULL UNIQUE CHECK (octet_length(command_id) BETWEEN 1 AND 120),
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    created_at timestamptz NOT NULL
);

CREATE INDEX moderation_decisions_case_idx
    ON public.moderation_decisions (case_id, created_at, id);

CREATE TABLE public.moderation_actions (
    id bigserial PRIMARY KEY,
    case_id bigint NOT NULL REFERENCES public.moderation_cases(id)
        ON DELETE RESTRICT,
    decision_id bigint NOT NULL REFERENCES public.moderation_decisions(id)
        ON DELETE RESTRICT,
    kind text NOT NULL CHECK (kind IN (
        'mark_scam', 'mark_fake', 'clear_peer_flags', 'freeze_account',
        'unfreeze_account', 'delete_private_message',
        'delete_channel_message', 'delete_account'
    )),
    payload jsonb NOT NULL CHECK (
        jsonb_typeof(payload) = 'object'
        AND octet_length(payload::text) <= 65536
    ),
    status text NOT NULL CHECK (status IN (
        'pending', 'processing', 'succeeded', 'superseded', 'retry', 'failed'
    )),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 20),
    available_at timestamptz NOT NULL,
    lease_until timestamptz,
    last_error text NOT NULL DEFAULT '' CHECK (char_length(last_error) <= 4000),
    command_id text NOT NULL UNIQUE CHECK (octet_length(command_id) BETWEEN 1 AND 160),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (updated_at >= created_at)
);

CREATE INDEX moderation_actions_claim_idx
    ON public.moderation_actions (available_at, id)
    WHERE status IN ('pending', 'retry', 'processing');

CREATE INDEX moderation_actions_case_idx
    ON public.moderation_actions (case_id, id);

CREATE TABLE public.moderation_appeals (
    id bigserial PRIMARY KEY,
    case_id bigint NOT NULL REFERENCES public.moderation_cases(id)
        ON DELETE RESTRICT,
    appellant_user_id bigint NOT NULL CHECK (appellant_user_id > 0),
    appeal_text text NOT NULL CHECK (char_length(appeal_text) BETWEEN 1 AND 4000),
    text_hash bytea NOT NULL CHECK (octet_length(text_hash) = 32),
    fingerprint bytea NOT NULL UNIQUE CHECK (octet_length(fingerprint) = 32),
    status text NOT NULL CHECK (status IN ('pending', 'granted', 'rejected')),
    previous_case_status text NOT NULL CHECK (
        previous_case_status IN ('resolved', 'dismissed')
    ),
    reviewer text NOT NULL DEFAULT '' CHECK (octet_length(reviewer) <= 128),
    review_reason text NOT NULL DEFAULT '' CHECK (char_length(review_reason) <= 2000),
    created_at timestamptz NOT NULL,
    reviewed_at timestamptz
);

CREATE UNIQUE INDEX moderation_appeals_one_pending_case_actor_idx
    ON public.moderation_appeals (case_id, appellant_user_id)
    WHERE status = 'pending';

CREATE INDEX moderation_appeals_queue_idx
    ON public.moderation_appeals (status, created_at, id);

CREATE TABLE public.moderation_appeal_links (
    id bigserial PRIMARY KEY,
    case_id bigint NOT NULL REFERENCES public.moderation_cases(id)
        ON DELETE RESTRICT,
    appellant_user_id bigint NOT NULL CHECK (appellant_user_id > 0),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at timestamptz NOT NULL,
    appeal_id bigint REFERENCES public.moderation_appeals(id)
        ON DELETE RESTRICT,
    created_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CHECK (expires_at > created_at),
    CHECK (expires_at <= created_at + interval '90 days'),
    CHECK (
        (appeal_id IS NULL AND consumed_at IS NULL)
        OR (appeal_id IS NOT NULL AND consumed_at IS NOT NULL)
    )
);

CREATE INDEX moderation_appeal_links_expiry_idx
    ON public.moderation_appeal_links (expires_at, id)
    WHERE consumed_at IS NULL;

CREATE INDEX moderation_appeal_links_case_idx
    ON public.moderation_appeal_links (case_id, id);

ALTER TABLE public.moderation_decisions
    ADD CONSTRAINT moderation_decisions_appeal_fk
    FOREIGN KEY (appeal_id) REFERENCES public.moderation_appeals(id)
    ON DELETE RESTRICT;

CREATE UNIQUE INDEX moderation_decisions_one_per_appeal_idx
    ON public.moderation_decisions (appeal_id)
    WHERE appeal_id IS NOT NULL;

-- Existing unified reports become one open case per target. This backfill is
-- deterministic and keeps every report linked exactly once.
INSERT INTO public.moderation_cases (
    target_peer_type, target_peer_id, status, severity, assigned_to,
    version, report_count, distinct_reporter_count, first_report_at,
    last_report_at, created_at, updated_at
)
SELECT
    target_peer_type,
    target_peer_id,
    'open',
    max(CASE reason
        WHEN 'child_abuse' THEN 4
        WHEN 'violence' THEN 3
        WHEN 'pornography' THEN 3
        WHEN 'illegal_drugs' THEN 3
        WHEN 'personal_details' THEN 3
        WHEN 'fake' THEN 2
        WHEN 'copyright' THEN 2
        ELSE 1
    END)::smallint,
    '',
    1,
    count(*)::integer,
    count(DISTINCT reporter_user_id)::integer,
    min(created_at),
    max(created_at),
    min(created_at),
    max(created_at)
FROM public.moderation_reports
GROUP BY target_peer_type, target_peer_id;

INSERT INTO public.moderation_case_reports (case_id, report_id, attached_at)
SELECT c.id, r.id, r.created_at
FROM public.moderation_reports r
JOIN public.moderation_cases c
  ON c.target_peer_type = r.target_peer_type
 AND c.target_peer_id = r.target_peer_id
 AND c.status = 'open';
