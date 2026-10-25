-- v2 编号 0236；移植自 main 0207_admin_console_users（main 0009095c），见 docs/main-v2-sync.md。
-- Admin console accounts.
--
-- Until now the panel had exactly one operator: TELESRV_ADMIN_UI_PASSWORD /
-- _TOKEN, signed into a session whose actor was the literal string "admin" and
-- whose rights were TELESRV_ADMIN_UI_PERMISSIONS (default "*"). That is kept as
-- a break-glass login -- an operator locked out of the database must still be
-- able to get in -- but named accounts now live here.
--
-- Named "admin_console_users" rather than "admin_users": admin_user_id already
-- means "the user who administers this chat/channel" across channel_invites and
-- friends, and reusing that noun for panel operators would read as the same
-- thing.
CREATE TABLE IF NOT EXISTS public.admin_console_users (
    id            bigserial PRIMARY KEY,
    username      text        NOT NULL,
    -- bcrypt. Never a reversible encoding: this column is the whole reason the
    -- table is worth protecting.
    password_hash text        NOT NULL,
    -- Permission names as understood by the panel's requirePermission. The
    -- single entry '*' is the wildcard.
    permissions   text[]      NOT NULL DEFAULT '{}',
    enabled       boolean     NOT NULL DEFAULT true,
    -- Bumped on every change that must not survive in an already-issued
    -- session: disabling the account, editing its permissions, changing its
    -- password. Sessions carry the epoch they were minted with and are refused
    -- once it no longer matches, which is what makes revocation immediate --
    -- rights live inside a signed cookie with a 12h TTL, so without this a
    -- demoted or disabled operator would keep their old access until it
    -- expired.
    token_epoch   integer     NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz
);

-- Usernames are compared case-insensitively so "Admin" and "admin" cannot be
-- two different operators -- a distinction that is invisible in a login form
-- and therefore a way to impersonate a colleague at a glance.
CREATE UNIQUE INDEX IF NOT EXISTS admin_console_users_username_lower_key
    ON public.admin_console_users (lower(username));