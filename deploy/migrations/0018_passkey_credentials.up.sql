-- Passkey (WebAuthn/FIDO2) credentials. Each row = one registered public key.
-- sign_count uses bigint to hold a uint32; credential_id is the primary key (raw
-- bytes, base64url externally).
CREATE TABLE public.passkey_credentials (
    credential_id bytea NOT NULL,
    user_id bigint NOT NULL,
    public_key bytea NOT NULL,
    sign_count bigint DEFAULT 0 NOT NULL,
    aaguid bytea DEFAULT '\x'::bytea NOT NULL,
    name character varying(128) DEFAULT ''::character varying NOT NULL,
    transports text[] DEFAULT '{}'::text[] NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    CONSTRAINT passkey_credentials_pkey PRIMARY KEY (credential_id),
    CONSTRAINT passkey_credentials_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES public.users(id) ON DELETE CASCADE
);

CREATE INDEX passkey_credentials_user_id_idx ON public.passkey_credentials (user_id);
