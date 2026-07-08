CREATE UNIQUE INDEX account_passwords_login_email_lower_unique_idx
    ON public.account_passwords (lower((login_email)::text))
    WHERE ((login_email)::text <> ''::text);
