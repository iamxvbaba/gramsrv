CREATE TABLE IF NOT EXISTS ai_compose_tones (
  id BIGINT PRIMARY KEY,
  access_hash BIGINT NOT NULL,
  owner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  slug TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  emoji_id BIGINT NOT NULL DEFAULT 0,
  prompt TEXT NOT NULL,
  display_author BOOLEAN NOT NULL DEFAULT FALSE,
  installs_count INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (id > 0),
  CHECK (access_hash <> 0),
  CHECK (owner_user_id > 0),
  CHECK (slug <> ''),
  CHECK (title <> ''),
  CHECK (prompt <> ''),
  CHECK (char_length(title) <= 12),
  CHECK (char_length(prompt) <= 1024),
  CHECK (installs_count >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS ai_compose_tones_access_hash_idx
  ON ai_compose_tones(id, access_hash);

CREATE INDEX IF NOT EXISTS ai_compose_tones_owner_updated_idx
  ON ai_compose_tones(owner_user_id, updated_at DESC, id);

CREATE TABLE IF NOT EXISTS ai_compose_tone_saves (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tone_id BIGINT NOT NULL REFERENCES ai_compose_tones(id) ON DELETE CASCADE,
  saved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, tone_id)
);

CREATE INDEX IF NOT EXISTS ai_compose_tone_saves_user_saved_idx
  ON ai_compose_tone_saves(user_id, saved_at, tone_id);
