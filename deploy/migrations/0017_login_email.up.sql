-- Login email: an alternate login method independent of the 2FA recovery email.
-- On a new-device login the code is delivered to this email instead
-- (auth.sentCodeTypeEmailCode). Only a confirmed real address is stored, and it
-- is masked when returned.
ALTER TABLE public.account_passwords
    ADD COLUMN login_email character varying(256) DEFAULT ''::character varying NOT NULL;
