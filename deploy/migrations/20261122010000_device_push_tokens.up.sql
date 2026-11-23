-- Device push tokens registered through account.registerDevice.
--
-- A token is globally identified by (token_type, token) and bound to at most one
-- account: the primary key makes re-registration an idempotent upsert, and a
-- token that moves to another account is reassigned rather than duplicated. The
-- push sender reads these rows only for users with no live MTProto session.
CREATE TABLE public.device_push_tokens (
    token_type INTEGER NOT NULL,
    token TEXT NOT NULL,
    user_id BIGINT NOT NULL,
    secret BYTEA,
    app_sandbox BOOLEAN NOT NULL DEFAULT FALSE,
    no_muted BOOLEAN NOT NULL DEFAULT FALSE,
    other_uids BIGINT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (token_type, token)
);

CREATE INDEX device_push_tokens_user_idx ON public.device_push_tokens (user_id);
