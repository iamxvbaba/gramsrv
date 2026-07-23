ALTER TABLE public.channels
	DROP CONSTRAINT IF EXISTS channels_scam_fake_mutually_exclusive,
	DROP COLUMN IF EXISTS scam,
	DROP COLUMN IF EXISTS fake;

ALTER TABLE public.users
	DROP CONSTRAINT IF EXISTS users_scam_fake_mutually_exclusive,
	DROP COLUMN IF EXISTS scam,
	DROP COLUMN IF EXISTS fake;
