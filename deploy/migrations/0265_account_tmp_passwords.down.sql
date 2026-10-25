-- Dropping the table revokes every outstanding temporary password; clients
-- ask for the 2FA password again and receive a new one.
DROP TABLE public.account_tmp_passwords;
