-- Layer 232 share themes (messages.setShareTheme). The default theme is the
-- absence of a row. A gift theme follows the gift: deleting the unique gift
-- drops the theme, and readers hide it once the peer no longer owns the gift.
CREATE TABLE peer_share_themes (
    peer_type      text        NOT NULL,
    peer_id        bigint      NOT NULL,
    kind           text        NOT NULL,
    backdrop_id    integer     NOT NULL DEFAULT 0,
    emojis         jsonb       NOT NULL DEFAULT '[]'::jsonb,
    unique_gift_id bigint      REFERENCES unique_star_gifts(id) ON DELETE CASCADE,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (peer_type, peer_id),
    CONSTRAINT peer_share_themes_peer_check
        CHECK (peer_type IN ('user', 'channel') AND peer_id > 0),
    CONSTRAINT peer_share_themes_shape_check CHECK (
        jsonb_typeof(emojis) = 'array' AND jsonb_array_length(emojis) <= 6 AND backdrop_id >= 0 AND
        ((kind = 'emoji_backdrop' AND unique_gift_id IS NULL AND
          (backdrop_id > 0 OR jsonb_array_length(emojis) > 0)) OR
         (kind = 'gift' AND unique_gift_id IS NOT NULL AND backdrop_id = 0 AND
          jsonb_array_length(emojis) = 0))
    )
);
