-- Official platform verification applications.
--
-- The application is the durable audit subject: it is never deleted, only moved
-- through its status machine, and every transition appends an immutable row to
-- verification_application_events. Decisions additionally go through the shared
-- admin command journal (admin_commands / admin_audit_logs), so the panel keeps
-- one audit story for all operator actions.
--
-- The target is addressed by its stable peer id. target_title / target_username
-- are a submission-time snapshot for the review queue and the audit trail,
-- because a username can move between peers and a title can change after filing.

CREATE TABLE public.verification_applications (
    id bigserial PRIMARY KEY,
    applicant_user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    target_type text NOT NULL CHECK (target_type IN ('bot', 'channel', 'supergroup', 'user')),
    target_id bigint NOT NULL CHECK (target_id > 0),
    target_title text NOT NULL DEFAULT '' CHECK (octet_length(target_title) <= 1024),
    target_username text NOT NULL DEFAULT '' CHECK (octet_length(target_username) <= 64),
    target_access_hash bigint NOT NULL DEFAULT 0,
    category text NOT NULL DEFAULT '' CHECK (octet_length(category) <= 64),
    description text NOT NULL DEFAULT '' CHECK (octet_length(description) <= 4096),
    official_website text NOT NULL DEFAULT '' CHECK (octet_length(official_website) <= 512),
    -- Links are stored as arrays rather than a child table: they are read and
    -- written as one whole, are bounded, and never need to be queried across
    -- applications.
    social_links text[] NOT NULL DEFAULT '{}' CHECK (cardinality(social_links) <= 10),
    press_links text[] NOT NULL DEFAULT '{}' CHECK (cardinality(press_links) <= 10),
    additional_note text NOT NULL DEFAULT '' CHECK (octet_length(additional_note) <= 4096),
    status text NOT NULL CHECK (status IN (
        'draft', 'submitted', 'in_review', 'approved', 'rejected', 'cancelled'
    )),
    reviewer_admin_id text NOT NULL DEFAULT '' CHECK (octet_length(reviewer_admin_id) <= 128),
    decision_reason text NOT NULL DEFAULT '' CHECK (octet_length(decision_reason) <= 4096),
    -- internal_note is operator-only and must never be projected to the applicant.
    internal_note text NOT NULL DEFAULT '' CHECK (octet_length(internal_note) <= 8192),
    correlation_id text NOT NULL DEFAULT '' CHECK (octet_length(correlation_id) <= 128),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    submitted_at timestamptz,
    reviewed_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK (updated_at >= created_at),
    -- A decided application always carries its reviewer and timestamp; a rejected
    -- one additionally carries the reason the applicant is told.
    CHECK (
        (status IN ('approved', 'rejected')) =
        (reviewed_at IS NOT NULL AND reviewer_admin_id <> '')
    ),
    CHECK (status <> 'rejected' OR decision_reason <> ''),
    CHECK (status = 'draft' OR submitted_at IS NOT NULL)
);

-- Exactly one live application per target. Draft, submitted and in_review are
-- the occupying states; decided and cancelled ones are history and do not block
-- a fresh attempt.
CREATE UNIQUE INDEX verification_applications_active_target_idx
    ON public.verification_applications (target_type, target_id)
    WHERE status IN ('draft', 'submitted', 'in_review');

-- One draft per applicant: the bot dialog is a single conversation, so a second
-- draft would have no way to be addressed.
CREATE UNIQUE INDEX verification_applications_applicant_draft_idx
    ON public.verification_applications (applicant_user_id)
    WHERE status = 'draft';

CREATE INDEX verification_applications_queue_idx
    ON public.verification_applications (status, created_at DESC, id DESC);

CREATE INDEX verification_applications_applicant_idx
    ON public.verification_applications (applicant_user_id, id DESC);

CREATE INDEX verification_applications_target_idx
    ON public.verification_applications (target_type, target_id, id DESC);

CREATE INDEX verification_applications_reviewer_idx
    ON public.verification_applications (reviewer_admin_id, reviewed_at DESC, id DESC)
    WHERE reviewer_admin_id <> '';

-- Username search in the review queue is a prefix match on the snapshot.
CREATE INDEX verification_applications_username_idx
    ON public.verification_applications (lower(target_username))
    WHERE target_username <> '';

-- Cooldown lookups after a rejection: newest decision per applicant+target.
CREATE INDEX verification_applications_cooldown_idx
    ON public.verification_applications (applicant_user_id, target_type, target_id, reviewed_at DESC)
    WHERE status = 'rejected';

-- Immutable per-application history. Rows are append-only: there is no UPDATE or
-- DELETE path in the store, and the panel renders this as the application
-- timeline.
CREATE TABLE public.verification_application_events (
    id bigserial PRIMARY KEY,
    application_id bigint NOT NULL REFERENCES public.verification_applications(id)
        ON DELETE RESTRICT,
    kind text NOT NULL CHECK (kind IN (
        'created', 'updated', 'submitted', 'claimed', 'approved', 'rejected',
        'cancelled', 'revoked', 'notified'
    )),
    from_status text NOT NULL DEFAULT '' CHECK (octet_length(from_status) <= 32),
    to_status text NOT NULL DEFAULT '' CHECK (octet_length(to_status) <= 32),
    actor text NOT NULL DEFAULT '' CHECK (octet_length(actor) <= 128),
    reason text NOT NULL DEFAULT '' CHECK (octet_length(reason) <= 4096),
    note text NOT NULL DEFAULT '' CHECK (octet_length(note) <= 8192),
    correlation_id text NOT NULL DEFAULT '' CHECK (octet_length(correlation_id) <= 128),
    created_at timestamptz NOT NULL
);

CREATE INDEX verification_application_events_app_idx
    ON public.verification_application_events (application_id, id DESC);

CREATE INDEX verification_application_events_actor_idx
    ON public.verification_application_events (actor, created_at DESC, id DESC)
    WHERE actor <> '';

-- Applicant notifications are delivered by @verifybot after the decision commits.
-- The outbox keeps that delivery exactly-once across restarts and makes a
-- repeated approve idempotent: the unique key is the decision, not the attempt.
CREATE TABLE public.verification_notification_outbox (
    id bigserial PRIMARY KEY,
    application_id bigint NOT NULL REFERENCES public.verification_applications(id)
        ON DELETE CASCADE,
    recipient_user_id bigint NOT NULL CHECK (recipient_user_id > 0),
    kind text NOT NULL CHECK (kind IN ('approved', 'rejected', 'revoked')),
    payload jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
        jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 8192
    ),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    delivered_at timestamptz,
    last_error text NOT NULL DEFAULT '' CHECK (octet_length(last_error) <= 1024),
    created_at timestamptz NOT NULL,
    CONSTRAINT verification_notification_once UNIQUE (application_id, kind)
);

CREATE INDEX verification_notification_pending_idx
    ON public.verification_notification_outbox (created_at, id)
    WHERE delivered_at IS NULL;
