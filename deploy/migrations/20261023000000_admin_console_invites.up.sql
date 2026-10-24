CREATE TABLE IF NOT EXISTS admin_console_invites (
    user_id BIGINT PRIMARY KEY REFERENCES admin_console_users(id) ON DELETE CASCADE,
    totp_secret TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL,
    single_use BOOLEAN NOT NULL DEFAULT TRUE,
    used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS admin_console_invites_expires_at_idx
    ON admin_console_invites (expires_at);
