CREATE SEQUENCE IF NOT EXISTS user_sticker_collections_order_key_seq;

ALTER TABLE user_sticker_collections ADD COLUMN order_key bigint;

WITH ordered AS (
  SELECT ctid, row_number() OVER (
    ORDER BY owner_user_id, kind, used_at, document_id
  ) AS n
  FROM user_sticker_collections
)
UPDATE user_sticker_collections AS c
SET order_key = ordered.n
FROM ordered
WHERE c.ctid = ordered.ctid;

SELECT setval(
  'user_sticker_collections_order_key_seq',
  GREATEST(COALESCE((SELECT max(order_key) FROM user_sticker_collections), 0), 1),
  true
);

ALTER TABLE user_sticker_collections
  ALTER COLUMN order_key SET DEFAULT nextval('user_sticker_collections_order_key_seq'),
  ALTER COLUMN order_key SET NOT NULL;

DROP INDEX IF EXISTS user_sticker_collections_order_idx;
CREATE INDEX user_sticker_collections_order_idx
  ON user_sticker_collections (owner_user_id, kind, order_key DESC);

-- Saved GIFs must reference canonical GIFv documents. Remove dangling/raw-GIF
-- entries explicitly instead of making getSavedGifs silently skip bad state.
DELETE FROM user_sticker_collections AS c
WHERE c.kind = 'gif'
  AND NOT EXISTS (
    SELECT 1
    FROM documents AS d
    WHERE d.id = c.document_id
      AND lower(d.mime_type) = 'video/mp4'
      AND EXISTS (
        SELECT 1 FROM jsonb_array_elements(d.attributes) a
        WHERE a->>'kind' = 'animated'
      )
      AND EXISTS (
        SELECT 1 FROM jsonb_array_elements(d.attributes) a
        WHERE a->>'kind' = 'video'
          AND COALESCE((a->>'round_message')::boolean, false) = false
          AND COALESCE((a->>'w')::int, 0) > 0
          AND COALESCE((a->>'h')::int, 0) > 0
          AND COALESCE((a->>'duration')::double precision, 0) > 0
      )
  );

-- Legacy raw image/gif rows are ordinary files, not TDesktop GIFv. Repair the
-- durable shared-media indexes without pretending their bytes were transcoded.
DELETE FROM message_box_media AS i
USING message_boxes AS b
WHERE i.owner_user_id = b.owner_user_id
  AND i.box_id = b.box_id
  AND i.category = 3
  AND lower(COALESCE(b.media #>> '{document,mime_type}', '')) = 'image/gif';

INSERT INTO message_box_media (owner_user_id, box_id, peer_id, category, message_date)
SELECT b.owner_user_id, b.box_id, b.peer_id, 4, b.message_date
FROM message_boxes AS b
WHERE NOT b.deleted
  AND lower(COALESCE(b.media #>> '{document,mime_type}', '')) = 'image/gif'
ON CONFLICT DO NOTHING;

DELETE FROM channel_message_media AS i
USING channel_messages AS m
WHERE i.channel_id = m.channel_id
  AND i.id = m.id
  AND i.category = 3
  AND lower(COALESCE(m.media #>> '{document,mime_type}', '')) = 'image/gif';

INSERT INTO channel_message_media (channel_id, id, category, message_date)
SELECT m.channel_id, m.id, 4, m.message_date
FROM channel_messages AS m
WHERE NOT m.deleted
  AND lower(COALESCE(m.media #>> '{document,mime_type}', '')) = 'image/gif'
ON CONFLICT DO NOTHING;
