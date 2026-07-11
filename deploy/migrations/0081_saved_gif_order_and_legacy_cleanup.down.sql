DROP INDEX IF EXISTS user_sticker_collections_order_idx;
CREATE INDEX user_sticker_collections_order_idx
  ON user_sticker_collections (owner_user_id, kind, used_at DESC);
ALTER TABLE user_sticker_collections DROP COLUMN IF EXISTS order_key;
DROP SEQUENCE IF EXISTS user_sticker_collections_order_key_seq;
