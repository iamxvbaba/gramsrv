-- gigagroup flag for supergroups (Layer 228 channel.gigagroup).
ALTER TABLE public.channels
	ADD COLUMN IF NOT EXISTS gigagroup boolean DEFAULT false NOT NULL;
