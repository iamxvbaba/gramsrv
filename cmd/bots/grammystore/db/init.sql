CREATE TABLE users (
  telegram_id BIGINT PRIMARY KEY,
  chat_id BIGINT NOT NULL,
  username TEXT NOT NULL DEFAULT '',
  first_name TEXT NOT NULL DEFAULT '',
  server_user_id BIGINT NOT NULL DEFAULT 0,
  language TEXT NOT NULL DEFAULT 'ru',
  notifications INTEGER NOT NULL DEFAULT 1,
  bonus INTEGER NOT NULL DEFAULT 0,
  referred_by BIGINT REFERENCES users(telegram_id),
  referral_count INTEGER NOT NULL DEFAULT 0,
  daily_day TEXT NOT NULL DEFAULT '',
  spin_day TEXT NOT NULL DEFAULT '',
  spin_day_count INTEGER NOT NULL DEFAULT 0,
  spin_week TEXT NOT NULL DEFAULT '',
  spin_week_count INTEGER NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
  updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE numbers (
  id SERIAL PRIMARY KEY,
  phone TEXT NOT NULL UNIQUE,
  display TEXT NOT NULL,
  format TEXT NOT NULL,
  country TEXT NOT NULL,
  owner_id BIGINT NOT NULL REFERENCES users(telegram_id),
  chat_id BIGINT NOT NULL,
  is_current BOOLEAN NOT NULL DEFAULT TRUE,
  retired BOOLEAN NOT NULL DEFAULT FALSE,
  login_code TEXT NOT NULL DEFAULT '',
  code_expires_at BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
  CONSTRAINT numbers_retired_current_check CHECK (NOT retired OR NOT is_current)
);

CREATE UNIQUE INDEX numbers_current_owner_idx ON numbers(owner_id) WHERE is_current = TRUE;

CREATE TABLE verified_phones (
  phone TEXT PRIMARY KEY,
  telegram_id BIGINT NOT NULL UNIQUE REFERENCES users(telegram_id),
  chat_id BIGINT NOT NULL,
  verified_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE code_access (
  phone TEXT NOT NULL,
  telegram_id BIGINT NOT NULL,
  PRIMARY KEY(phone, telegram_id)
);

CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE pending (
  telegram_id BIGINT PRIMARY KEY,
  kind TEXT NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}',
  updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE processed_payments (
  charge_id TEXT PRIMARY KEY,
  telegram_id BIGINT NOT NULL,
  invoice_payload TEXT NOT NULL,
  amount INTEGER NOT NULL,
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE sales (
  id SERIAL PRIMARY KEY,
  created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
  product TEXT NOT NULL,
  title TEXT NOT NULL,
  stars_price INTEGER NOT NULL,
  recipient_id BIGINT NOT NULL,
  buyer_id BIGINT NOT NULL,
  buyer_name TEXT NOT NULL DEFAULT '',
  charge_id TEXT NOT NULL UNIQUE,
  fulfillment_json JSONB NOT NULL DEFAULT '{}'
);

CREATE TABLE recent_recipients (
  buyer_id BIGINT NOT NULL,
  recipient_id BIGINT NOT NULL,
  used_at BIGINT NOT NULL,
  PRIMARY KEY(buyer_id, recipient_id)
);

CREATE TABLE promos (
  code TEXT PRIMARY KEY,
  stars_amount INTEGER NOT NULL,
  max_acts INTEGER NOT NULL,
  activations INTEGER NOT NULL DEFAULT 0,
  active BOOLEAN NOT NULL DEFAULT TRUE,
  created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE promo_claims (
  code TEXT NOT NULL REFERENCES promos(code),
  telegram_id BIGINT NOT NULL,
  claimed_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
  PRIMARY KEY(code, telegram_id)
);

CREATE TABLE giveaways (
  id TEXT PRIMARY KEY,
  text TEXT NOT NULL,
  stars_amount INTEGER NOT NULL,
  max_acts INTEGER NOT NULL,
  activations INTEGER NOT NULL DEFAULT 0,
  active BOOLEAN NOT NULL DEFAULT TRUE,
  created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE giveaway_claims (
  giveaway_id TEXT NOT NULL REFERENCES giveaways(id),
  telegram_id BIGINT NOT NULL,
  claimed_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
  PRIMARY KEY(giveaway_id, telegram_id)
);

CREATE TABLE support_messages (
  id SERIAL PRIMARY KEY,
  telegram_id BIGINT NOT NULL,
  chat_id BIGINT NOT NULL,
  text TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
  answered_at BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE refunds (
  charge_id TEXT PRIMARY KEY,
  telegram_id BIGINT NOT NULL,
  refunded_at BIGINT NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'completed',
  internal_reversed BOOLEAN NOT NULL DEFAULT FALSE,
  error TEXT NOT NULL DEFAULT '',
  updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

CREATE TABLE spin_awards (
  telegram_id BIGINT NOT NULL,
  day TEXT NOT NULL,
  week TEXT NOT NULL,
  server_user_id BIGINT NOT NULL,
  prize INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
  PRIMARY KEY(telegram_id, day)
);

CREATE TABLE otp_deliveries (
  delivery_id TEXT PRIMARY KEY,
  fingerprint TEXT NOT NULL,
  recipient TEXT NOT NULL,
  code TEXT NOT NULL,
  expires_at BIGINT NOT NULL,
  accepted_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
);

INSERT INTO settings (key, value) VALUES
  ('stars_rate', '20')
ON CONFLICT (key) DO NOTHING;
