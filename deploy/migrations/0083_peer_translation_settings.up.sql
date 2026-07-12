CREATE TABLE peer_translation_settings (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    peer_type text NOT NULL CHECK (peer_type IN ('user', 'channel')),
    peer_id bigint NOT NULL CHECK (peer_id > 0),
    disabled boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, peer_type, peer_id)
);

CREATE INDEX peer_translation_settings_peer_idx
    ON peer_translation_settings (peer_type, peer_id, user_id);
