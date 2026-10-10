-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
CREATE SEQUENCE welcome_message_join_event_id_seq AS bigint;

CREATE TABLE welcome_message_deliveries (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    join_event_id bigint NOT NULL,
    channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    target_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    template_id integer NOT NULL,
    ephemeral_id integer GENERATED ALWAYS AS (((id - 1) % 2147483646 + 1)::integer) STORED,
    joined_at integer NOT NULL,
    content jsonb NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_expires_at timestamptz,
    delivered_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
    UNIQUE (join_event_id, template_id),
    UNIQUE (target_user_id, ephemeral_id),
    CONSTRAINT welcome_message_deliveries_shape CHECK (
        join_event_id > 0 AND channel_id > 0 AND target_user_id > 0 AND
        template_id > 0 AND ephemeral_id > 0 AND joined_at > 0 AND
        jsonb_typeof(content) = 'object' AND pg_column_size(content) <= 4194304 AND
        attempt_count >= 0 AND char_length(lease_owner) <= 128 AND
        char_length(last_error) <= 1024 AND expires_at > created_at AND
        expires_at <= created_at + interval '24 hours 1 second'
    )
);

CREATE INDEX welcome_message_deliveries_due_idx
    ON welcome_message_deliveries (next_attempt_at, id)
    WHERE delivered_at IS NULL;

CREATE INDEX welcome_message_deliveries_expiry_idx
    ON welcome_message_deliveries (expires_at, id);
