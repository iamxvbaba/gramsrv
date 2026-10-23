-- Layer 232 free-messages links (contacts.exportNoPaidMessagesToken /
-- contacts.importNoPaidMessagesToken). At most one active permanent and one
-- active temporary token per owner; revoked rows are kept for audit.
CREATE TABLE no_paid_messages_tokens (
    id            bigserial PRIMARY KEY,
    owner_user_id bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token         text        NOT NULL UNIQUE,
    temporary     boolean     NOT NULL,
    expires_at    timestamptz,
    created_at    timestamptz NOT NULL,
    revoked_at    timestamptz,
    CONSTRAINT no_paid_messages_tokens_expiry_check
        CHECK (temporary = (expires_at IS NOT NULL)),
    CONSTRAINT no_paid_messages_tokens_token_check
        CHECK (octet_length(token) BETWEEN 16 AND 64)
);

CREATE UNIQUE INDEX no_paid_messages_tokens_active_uniq
    ON no_paid_messages_tokens (owner_user_id, temporary)
    WHERE revoked_at IS NULL;
