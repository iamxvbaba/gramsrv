-- Telegram Login / OIDC is one durable authorization aggregate shared by the
-- public HTTP provider and MTProto URL-auth RPCs. PostgreSQL is authoritative;
-- Redis/NOTIFY may wake waiters but may not own any transition below.
UPDATE public.bots
SET commands = commands || '[
  {"command":"setlogin","description":"configure Telegram Login"},
  {"command":"logininfo","description":"show Telegram Login configuration"},
  {"command":"resetloginsecret","description":"rotate an OIDC Client Secret"}
]'::jsonb,
    updated_at = now()
WHERE bot_user_id = 93372553;

CREATE TABLE public.bot_login_clients (
    bot_user_id bigint PRIMARY KEY REFERENCES public.bots(bot_user_id) ON DELETE CASCADE,
    client_id text NOT NULL UNIQUE,
    client_secret_hash bytea NOT NULL,
    secret_version bigint DEFAULT 1 NOT NULL,
    signing_algorithm text DEFAULT 'RS256'::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT bot_login_clients_client_id_check
        CHECK (client_id = bot_user_id::text AND length(client_id) BETWEEN 1 AND 64),
    CONSTRAINT bot_login_clients_secret_hash_check CHECK (octet_length(client_secret_hash) = 32),
    CONSTRAINT bot_login_clients_secret_version_check CHECK (secret_version > 0),
    CONSTRAINT bot_login_clients_signing_algorithm_check
        CHECK (signing_algorithm IN ('RS256','ES256','EdDSA','ES256K'))
);

CREATE TABLE public.bot_login_allowed_urls (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    bot_user_id bigint NOT NULL REFERENCES public.bot_login_clients(bot_user_id) ON DELETE CASCADE,
    kind text NOT NULL,
    normalized_url text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT bot_login_allowed_urls_kind_check CHECK (kind IN ('web_origin','redirect_uri')),
    CONSTRAINT bot_login_allowed_urls_value_check CHECK (length(normalized_url) BETWEEN 1 AND 4096),
    UNIQUE (bot_user_id, kind, normalized_url)
);

CREATE INDEX bot_login_allowed_urls_bot_page_idx
    ON public.bot_login_allowed_urls(bot_user_id, kind, id);

CREATE TABLE public.bot_login_native_apps (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    bot_user_id bigint NOT NULL REFERENCES public.bot_login_clients(bot_user_id) ON DELETE CASCADE,
    platform text NOT NULL,
    application_id text NOT NULL,
    verification_id text NOT NULL,
    callback_uri text NOT NULL,
    verified_display_name text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT bot_login_native_apps_platform_check CHECK (platform IN ('ios','android')),
    CONSTRAINT bot_login_native_apps_app_id_check
        CHECK (length(application_id) BETWEEN 3 AND 255 AND application_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]*$'),
    CONSTRAINT bot_login_native_apps_verification_check CHECK (
        (platform = 'ios' AND verification_id ~ '^[A-Z0-9]{10}$')
        OR (platform = 'android' AND verification_id ~ '^[0-9A-F]{64}$')
    ),
    CONSTRAINT bot_login_native_apps_callback_check CHECK (length(callback_uri) BETWEEN 1 AND 4096),
    CONSTRAINT bot_login_native_apps_name_check CHECK (length(btrim(verified_display_name)) BETWEEN 1 AND 128),
    UNIQUE (bot_user_id, platform, application_id, verification_id),
    UNIQUE (bot_user_id, callback_uri)
);

CREATE INDEX bot_login_native_apps_bot_page_idx
    ON public.bot_login_native_apps(bot_user_id, id);
CREATE INDEX bot_login_native_apps_callback_idx
    ON public.bot_login_native_apps(bot_user_id, callback_uri) WHERE enabled;

CREATE TABLE public.telegram_login_requests (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_token_hash bytea NOT NULL UNIQUE,
    browser_token_hash bytea NOT NULL UNIQUE,
    bot_user_id bigint NOT NULL REFERENCES public.bot_login_clients(bot_user_id) ON DELETE CASCADE,
    client_id text NOT NULL,
    signing_algorithm text NOT NULL,
    source text NOT NULL,
    response_type text NOT NULL,
    redirect_uri text NOT NULL,
    origin text DEFAULT ''::text NOT NULL,
    domain text NOT NULL,
    requested_scopes text[] NOT NULL,
    oauth_state text DEFAULT ''::text NOT NULL,
    nonce text DEFAULT ''::text NOT NULL,
    code_challenge text NOT NULL,
    code_challenge_method text NOT NULL,
    browser text NOT NULL,
    platform text NOT NULL,
    ip text NOT NULL,
    region text NOT NULL,
    in_app_origin text DEFAULT ''::text NOT NULL,
    is_app boolean DEFAULT false NOT NULL,
    verified_app_name text DEFAULT ''::text NOT NULL,
    match_codes text[] DEFAULT '{}'::text[] NOT NULL,
    match_code text DEFAULT ''::text NOT NULL,
    match_codes_first boolean DEFAULT false NOT NULL,
    user_id_hint bigint DEFAULT 0 NOT NULL,
    peer_type text DEFAULT ''::text NOT NULL,
    peer_id bigint DEFAULT 0 NOT NULL,
    message_id integer DEFAULT 0 NOT NULL,
    button_id integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    authorized_user_id bigint REFERENCES public.users(id),
    profile_name text DEFAULT ''::text NOT NULL,
    given_name text DEFAULT ''::text NOT NULL,
    family_name text DEFAULT ''::text NOT NULL,
    preferred_username text DEFAULT ''::text NOT NULL,
    picture text DEFAULT ''::text NOT NULL,
    phone_number text DEFAULT ''::text NOT NULL,
    write_allowed boolean DEFAULT false NOT NULL,
    phone_shared boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    approved_at timestamp with time zone,
    declined_at timestamp with time zone,
    CONSTRAINT telegram_login_requests_hashes_check
        CHECK (octet_length(request_token_hash) = 32 AND octet_length(browser_token_hash) = 32),
    CONSTRAINT telegram_login_requests_signing_algorithm_check
        CHECK (signing_algorithm IN ('RS256','ES256','EdDSA','ES256K')),
    CONSTRAINT telegram_login_requests_source_check
        CHECK (source IN ('web','javascript','native','mini_app','message_button')),
    CONSTRAINT telegram_login_requests_response_type_check CHECK (response_type IN ('code','post_message','legacy_url')),
    CONSTRAINT telegram_login_requests_source_response_check CHECK (
        (source = 'web' AND response_type = 'code')
        OR (source = 'javascript' AND response_type = 'post_message')
        OR (source = 'native' AND response_type = 'code')
        OR (source = 'mini_app' AND response_type = 'post_message')
        OR (source = 'message_button' AND response_type = 'legacy_url')
    ),
    CONSTRAINT telegram_login_requests_url_check
        CHECK (length(redirect_uri) BETWEEN 1 AND 4096 AND length(origin) <= 4096
            AND length(domain) BETWEEN 1 AND 255 AND length(in_app_origin) <= 4096),
    CONSTRAINT telegram_login_requests_scope_check
        CHECK (cardinality(requested_scopes) BETWEEN 1 AND 4 AND requested_scopes @> ARRAY['openid']::text[]),
    CONSTRAINT telegram_login_requests_oauth_value_check
        CHECK (length(oauth_state) <= 2048 AND length(nonce) <= 1024),
    CONSTRAINT telegram_login_requests_pkce_check CHECK (
        (response_type = 'code' AND code_challenge_method = 'S256' AND length(code_challenge) BETWEEN 43 AND 128)
        OR (response_type = 'post_message' AND (
            (code_challenge = '' AND code_challenge_method = '')
            OR (code_challenge_method = 'S256' AND length(code_challenge) BETWEEN 43 AND 128)))
        OR (response_type = 'legacy_url' AND code_challenge = '' AND code_challenge_method = '')
    ),
    CONSTRAINT telegram_login_requests_device_check
        CHECK (length(browser) BETWEEN 1 AND 255 AND length(platform) BETWEEN 1 AND 255 AND length(ip) BETWEEN 1 AND 128 AND length(region) BETWEEN 1 AND 255),
    CONSTRAINT telegram_login_requests_match_codes_check
        CHECK (cardinality(match_codes) <= 8
            AND (cardinality(match_codes) = 0 OR (match_code <> '' AND match_code = ANY(match_codes)))
            AND (NOT match_codes_first OR cardinality(match_codes) > 0)),
    CONSTRAINT telegram_login_requests_context_check
        CHECK (user_id_hint >= 0 AND peer_id >= 0 AND message_id >= 0 AND button_id >= 0),
    CONSTRAINT telegram_login_requests_app_shape_check CHECK (
        (source = 'native' AND is_app AND verified_app_name <> '' AND origin = '')
        OR (source <> 'native' AND NOT is_app AND verified_app_name = '' AND origin <> '')
    ),
    CONSTRAINT telegram_login_requests_in_app_shape_check CHECK (
        (source = 'mini_app' AND response_type = 'post_message'
            AND in_app_origin <> '' AND origin = in_app_origin)
        OR (source <> 'mini_app' AND in_app_origin = '')
    ),
    CONSTRAINT telegram_login_requests_consent_scope_check CHECK (
        (NOT write_allowed OR 'telegram:bot_access' = ANY(requested_scopes))
        AND (NOT phone_shared OR 'phone' = ANY(requested_scopes))
        AND ((phone_shared AND phone_number <> '') OR (NOT phone_shared AND phone_number = ''))
    ),
    CONSTRAINT telegram_login_requests_status_check CHECK (status IN ('pending','approved','declined','expired')),
    CONSTRAINT telegram_login_requests_claims_check CHECK (
        length(profile_name) <= 255 AND length(given_name) <= 255 AND length(family_name) <= 255
        AND length(preferred_username) <= 64 AND length(picture) <= 4096 AND length(phone_number) <= 32
    ),
    CONSTRAINT telegram_login_requests_time_check CHECK (expires_at > created_at),
    CONSTRAINT telegram_login_requests_terminal_shape_check CHECK (
        (status = 'pending' AND authorized_user_id IS NULL AND profile_name = '' AND given_name = ''
            AND family_name = '' AND preferred_username = '' AND picture = '' AND phone_number = ''
            AND NOT write_allowed AND NOT phone_shared AND approved_at IS NULL AND declined_at IS NULL)
        OR (status = 'approved' AND authorized_user_id IS NOT NULL AND approved_at IS NOT NULL AND declined_at IS NULL
            AND (('profile' = ANY(requested_scopes) AND profile_name <> '' AND given_name <> '')
                 OR (NOT ('profile' = ANY(requested_scopes)) AND profile_name = '' AND given_name = ''
                     AND family_name = '' AND preferred_username = '' AND picture = '')))
        OR (status = 'declined' AND authorized_user_id IS NULL AND profile_name = '' AND given_name = ''
            AND family_name = '' AND preferred_username = '' AND picture = '' AND phone_number = ''
            AND NOT write_allowed AND NOT phone_shared AND approved_at IS NULL AND declined_at IS NOT NULL)
        OR (status = 'expired' AND authorized_user_id IS NULL AND profile_name = '' AND given_name = ''
            AND family_name = '' AND preferred_username = '' AND picture = '' AND phone_number = ''
            AND NOT write_allowed AND NOT phone_shared AND approved_at IS NULL AND declined_at IS NULL)
    )
);

CREATE INDEX telegram_login_requests_expiry_idx
    ON public.telegram_login_requests(expires_at, id) WHERE status = 'pending';
CREATE INDEX telegram_login_requests_user_active_idx
    ON public.telegram_login_requests(authorized_user_id, approved_at DESC, id DESC)
    WHERE status = 'approved';

CREATE TABLE public.web_authorizations (
    hash bigint PRIMARY KEY,
    request_id bigint NOT NULL UNIQUE REFERENCES public.telegram_login_requests(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    bot_user_id bigint NOT NULL REFERENCES public.bots(bot_user_id) ON DELETE CASCADE,
    domain text NOT NULL,
    browser text NOT NULL,
    platform text NOT NULL,
    ip text NOT NULL,
    region text NOT NULL,
    granted_scopes text[] NOT NULL,
    phone_shared boolean DEFAULT false NOT NULL,
    bot_access_granted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone NOT NULL,
    last_active_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    CONSTRAINT web_authorizations_hash_check CHECK (hash <> 0),
    CONSTRAINT web_authorizations_identity_check CHECK (user_id > 0 AND bot_user_id > 0),
    CONSTRAINT web_authorizations_text_check
        CHECK (length(domain) BETWEEN 1 AND 255 AND length(browser) BETWEEN 1 AND 255
            AND length(platform) BETWEEN 1 AND 255 AND length(ip) BETWEEN 1 AND 128
            AND length(region) BETWEEN 1 AND 255),
    CONSTRAINT web_authorizations_scope_check
        CHECK (cardinality(granted_scopes) BETWEEN 1 AND 4 AND granted_scopes @> ARRAY['openid']::text[]
            AND (NOT phone_shared OR 'phone' = ANY(granted_scopes))
            AND (NOT bot_access_granted OR 'telegram:bot_access' = ANY(granted_scopes))),
    CONSTRAINT web_authorizations_time_check
        CHECK (last_active_at >= created_at AND (revoked_at IS NULL OR revoked_at >= created_at))
);

CREATE INDEX web_authorizations_user_active_page_idx
    ON public.web_authorizations(user_id, last_active_at DESC, hash DESC)
    WHERE revoked_at IS NULL;
CREATE INDEX web_authorizations_bot_active_idx
    ON public.web_authorizations(bot_user_id, user_id, hash)
    WHERE revoked_at IS NULL;

CREATE TABLE public.telegram_login_codes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id bigint NOT NULL UNIQUE REFERENCES public.telegram_login_requests(id) ON DELETE CASCADE,
    code_hash bytea NOT NULL UNIQUE,
    sealed_code bytea NOT NULL,
    seal_nonce bytea NOT NULL,
    seal_key_id text NOT NULL,
    issued_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    CONSTRAINT telegram_login_codes_hash_check CHECK (octet_length(code_hash) = 32),
    CONSTRAINT telegram_login_codes_sealed_check
        CHECK (octet_length(sealed_code) >= 32 AND octet_length(seal_nonce) >= 12 AND length(seal_key_id) BETWEEN 1 AND 128),
    CONSTRAINT telegram_login_codes_time_check
        CHECK (expires_at > issued_at AND (consumed_at IS NULL OR (consumed_at >= issued_at AND consumed_at < expires_at)))
);

CREATE INDEX telegram_login_codes_expiry_idx
    ON public.telegram_login_codes(expires_at, id) WHERE consumed_at IS NULL;
