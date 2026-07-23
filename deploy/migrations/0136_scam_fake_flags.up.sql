-- SCAM / FAKE moderation flags for users (incl. bots) and channels.
-- Mirrors the Layer 228 user.scam/user.fake and channel.scam/channel.fake TL flags.
ALTER TABLE public.users
	ADD COLUMN IF NOT EXISTS scam boolean DEFAULT false NOT NULL,
	ADD COLUMN IF NOT EXISTS fake boolean DEFAULT false NOT NULL;

ALTER TABLE public.channels
	ADD COLUMN IF NOT EXISTS scam boolean DEFAULT false NOT NULL,
	ADD COLUMN IF NOT EXISTS fake boolean DEFAULT false NOT NULL;
