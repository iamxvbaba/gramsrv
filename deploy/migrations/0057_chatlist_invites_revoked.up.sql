ALTER TABLE public.chatlist_invites
    ADD COLUMN IF NOT EXISTS revoked boolean NOT NULL DEFAULT false;
