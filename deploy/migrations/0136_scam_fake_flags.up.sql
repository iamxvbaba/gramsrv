-- SCAM / FAKE moderation flags for users (incl. bots) and channels.
-- Mirrors the Layer 228 user.scam/user.fake and channel.scam/channel.fake TL flags.
ALTER TABLE public.users
	ADD COLUMN IF NOT EXISTS scam boolean DEFAULT false NOT NULL,
	ADD COLUMN IF NOT EXISTS fake boolean DEFAULT false NOT NULL;

UPDATE public.users SET fake = false WHERE scam AND fake;
ALTER TABLE public.users
	DROP CONSTRAINT IF EXISTS users_scam_fake_mutually_exclusive,
	ADD CONSTRAINT users_scam_fake_mutually_exclusive CHECK (NOT (scam AND fake));

ALTER TABLE public.channels
	ADD COLUMN IF NOT EXISTS scam boolean DEFAULT false NOT NULL,
	ADD COLUMN IF NOT EXISTS fake boolean DEFAULT false NOT NULL;

UPDATE public.channels SET fake = false WHERE scam AND fake;
ALTER TABLE public.channels
	DROP CONSTRAINT IF EXISTS channels_scam_fake_mutually_exclusive,
	ADD CONSTRAINT channels_scam_fake_mutually_exclusive CHECK (NOT (scam AND fake));
