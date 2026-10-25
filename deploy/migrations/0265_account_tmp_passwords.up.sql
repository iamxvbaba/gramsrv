-- v2 编号 0265；移植自 main 0241_account_tmp_passwords（main 8fce8c54，B14），见 docs/main-v2-sync.md。
-- account.getTmpPassword grants. A grant belongs to one permanent
-- authorization: issuing again replaces it, and deleting the authorization
-- (logout, session reset, account deletion) deletes it. Only the SHA-256 of
-- the bearer secret is stored.
--
-- authorization_created_at and password_changed_at are the version tokens the
-- grant was issued under. A re-login on the same auth key or any 2FA change
-- moves those columns on authorizations / account_passwords, and the check
-- rejects a grant whose tokens no longer match, so the grant is revoked before
-- its row is replaced or removed.
CREATE TABLE public.account_tmp_passwords (
    auth_key_id bigint PRIMARY KEY
        REFERENCES public.authorizations(auth_key_id) ON DELETE CASCADE,
    user_id bigint NOT NULL,
    authorization_created_at timestamp with time zone NOT NULL,
    password_changed_at timestamp with time zone NOT NULL,
    secret_hash bytea NOT NULL,
    valid_until timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT account_tmp_passwords_identity_check CHECK (auth_key_id <> 0 AND user_id <> 0),
    CONSTRAINT account_tmp_passwords_secret_hash_check CHECK (octet_length(secret_hash) = 32)
);
