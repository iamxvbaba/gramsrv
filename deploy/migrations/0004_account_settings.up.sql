-- Account-level singleton settings (one row per user): global privacy switches,
-- account self-destruct TTL, sensitive-content switches, contact sign-up
-- notification mute. Previously these account.* RPCs were hardcoded echo stubs
-- (not persisted): account.get/setGlobalPrivacySettings, get/setAccountTTL,
-- get/setContentSettings, get/setContactSignUpNotification. This table provides
-- real persistence.
CREATE TABLE public.account_settings (
    user_id bigint NOT NULL,
    archive_and_mute_new_noncontact_peers boolean DEFAULT false NOT NULL,
    keep_archived_unmuted boolean DEFAULT false NOT NULL,
    keep_archived_folders boolean DEFAULT false NOT NULL,
    hide_read_marks boolean DEFAULT false NOT NULL,
    new_noncontact_peers_require_premium boolean DEFAULT false NOT NULL,
    display_gifts_button boolean DEFAULT false NOT NULL,
    noncontact_peers_paid_stars bigint DEFAULT 0 NOT NULL,
    account_ttl_days integer DEFAULT 365 NOT NULL,
    sensitive_content_enabled boolean DEFAULT false NOT NULL,
    contact_signup_silent boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT account_settings_account_ttl_days_check CHECK ((account_ttl_days > 0)),
    CONSTRAINT account_settings_noncontact_peers_paid_stars_check CHECK ((noncontact_peers_paid_stars >= 0))
);

ALTER TABLE ONLY public.account_settings
    ADD CONSTRAINT account_settings_pkey PRIMARY KEY (user_id);
