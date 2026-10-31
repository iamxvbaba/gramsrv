-- sendMultiMedia must durably reserve grouped_id before resolving uploaded media
-- and sending item by item. This table pins each send random_id to an album group
-- within the conversation scope, so that after a mid-way failure, when the client
-- retries only the failed subset, it can still recover the grouped_id used by the
-- first full batch; intent_hash also prevents the same random_id with changed
-- content from being wrongly merged into the old reservation.
CREATE TABLE album_group_reservations (
    sender_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    peer_type TEXT NOT NULL CHECK (peer_type IN ('user', 'channel')),
    peer_id BIGINT NOT NULL CHECK (peer_id > 0),
    random_id BIGINT NOT NULL CHECK (random_id <> 0),
    intent_hash BYTEA NOT NULL CHECK (octet_length(intent_hash) = 32),
    grouped_id BIGINT NOT NULL CHECK (grouped_id <> 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (sender_user_id, peer_type, peer_id, random_id)
);

COMMENT ON TABLE album_group_reservations IS
    'Durable pre-send binding from album item random_id to grouped_id; never reconstructed from a retry subset.';
