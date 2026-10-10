-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
CREATE TABLE welcome_message_peers (
    channel_id bigint PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    next_id integer NOT NULL DEFAULT 1,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT welcome_message_peers_shape CHECK (
        channel_id > 0 AND next_id > 0 AND next_id <= 2147483647 AND revision > 0
    )
);

CREATE TABLE welcome_messages (
    channel_id bigint NOT NULL REFERENCES welcome_message_peers(channel_id) ON DELETE CASCADE,
    id integer NOT NULL,
    creator_user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    date integer NOT NULL,
    edit_date integer NOT NULL DEFAULT 0,
    random_id bigint NOT NULL,
    content jsonb NOT NULL,
    create_fingerprint bytea NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, id),
    UNIQUE (channel_id, creator_user_id, random_id),
    CONSTRAINT welcome_messages_shape CHECK (
        channel_id > 0 AND id > 0 AND creator_user_id > 0 AND date > 0 AND
        edit_date >= 0 AND (edit_date = 0 OR edit_date >= date) AND random_id <> 0 AND
        jsonb_typeof(content) = 'object' AND pg_column_size(content) <= 4194304 AND
        octet_length(create_fingerprint) = 32 AND version > 0
    )
);
