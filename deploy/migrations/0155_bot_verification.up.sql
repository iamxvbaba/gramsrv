-- Third-party bot verification (core.telegram.org/api/bots/verification).
--
-- This is a SEPARATE mechanism from the official platform badge implemented in
-- 0153/0154. Official verification is a boolean on the peer that only the
-- operator can set and that clients render as the standard checkmark. Third-party
-- verification is an attributed mark granted by a verifier bot: it carries that
-- verifier's own custom-emoji icon and a human-readable description, renders
-- BEFORE the name, and never becomes the official checkmark. Both can coexist on
-- one peer, and neither reads the other's tables.
--
-- Layer 228 surfaces:
--   user#b1b8cc83        bot_verification_icon:flags2.14?long
--   channel#d49f34c6     bot_verification_icon:flags2.13?long
--   userFull#6cbe645     bot_verification:flags2.12?BotVerification
--   channelFull#a04e8d3a bot_verification:flags2.17?BotVerification
--   chatInvite#5c9d3702  bot_verification:flags.13?BotVerification
--   botInfo#4d8a0299     verifier_settings:flags.9?BotVerifierSettings

-- The icon catalogue. An icon is a custom emoji document the client resolves with
-- messages.getCustomEmojiDocuments, so document_id must name a real document:
-- clients render nothing for an id they cannot fetch.
CREATE TABLE public.verification_icons (
    id bigserial PRIMARY KEY,
    document_id bigint NOT NULL UNIQUE CHECK (document_id > 0),
    -- owner_bot_id is 0 for a shared catalogue entry any verifier may use, and a
    -- bot id when the operator reserved the icon for one verifier.
    owner_bot_id bigint NOT NULL DEFAULT 0 CHECK (owner_bot_id >= 0),
    name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 512),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (updated_at >= created_at)
);

CREATE INDEX verification_icons_active_idx
    ON public.verification_icons (active, id DESC);

CREATE INDEX verification_icons_owner_idx
    ON public.verification_icons (owner_bot_id, id DESC)
    WHERE owner_bot_id <> 0;

-- Verifier status. A row here is what makes a bot a verifier: it is projected as
-- botInfo.verifier_settings and is the only authority bots.setCustomVerification
-- consults, which is why granting it is an operator action and never a bot one.
CREATE TABLE public.bot_verifier_settings (
    bot_id bigint PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    icon_document_id bigint NOT NULL CHECK (icon_document_id > 0),
    company_name text NOT NULL CHECK (octet_length(company_name) BETWEEN 1 AND 512),
    default_description text NOT NULL DEFAULT '' CHECK (octet_length(default_description) <= 280),
    -- can_modify_custom_description mirrors the TL flag: when false the verifier
    -- may only apply default_description, so a per-peer description cannot be
    -- smuggled past the operator.
    can_modify_custom_description boolean NOT NULL DEFAULT false,
    enabled boolean NOT NULL DEFAULT true,
    granted_by text NOT NULL DEFAULT '' CHECK (octet_length(granted_by) <= 128),
    grant_reason text NOT NULL DEFAULT '' CHECK (octet_length(grant_reason) <= 4096),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK (updated_at >= created_at)
);

CREATE INDEX bot_verifier_settings_enabled_idx
    ON public.bot_verifier_settings (enabled, bot_id);

-- Granted marks. The wire model carries one BotVerification per peer, so a new
-- verifier replaces the previous mark instead of leaving hidden rows that could
-- reappear after a later revocation.
CREATE TABLE public.custom_verifications (
    id bigserial PRIMARY KEY,
    verifier_bot_id bigint NOT NULL REFERENCES public.bot_verifier_settings(bot_id)
        ON DELETE CASCADE,
    peer_type text NOT NULL CHECK (peer_type IN ('user', 'channel')),
    peer_id bigint NOT NULL CHECK (peer_id > 0),
    -- icon_document_id is denormalised from the verifier at grant time: the mark
    -- must keep rendering the icon it was granted with even if the verifier later
    -- changes its own.
    icon_document_id bigint NOT NULL CHECK (icon_document_id > 0),
    description text NOT NULL DEFAULT '' CHECK (octet_length(description) <= 4096),
    granted_by_user_id bigint NOT NULL DEFAULT 0 CHECK (granted_by_user_id >= 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CONSTRAINT custom_verifications_peer_once UNIQUE (peer_type, peer_id),
    CHECK (updated_at >= created_at)
);

-- Projection and ownership lookup by peer.
CREATE INDEX custom_verifications_peer_idx
    ON public.custom_verifications (peer_type, peer_id, id DESC);

CREATE INDEX custom_verifications_verifier_idx
    ON public.custom_verifications (verifier_bot_id, id DESC);

-- Applications a peer files with a verifier bot. The mark itself lives in
-- custom_verifications; this is the review queue in front of it, so a rejected or
-- revoked application stays as history without implying a mark.
CREATE TABLE public.custom_verification_requests (
    id bigserial PRIMARY KEY,
    verifier_bot_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    applicant_user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    peer_type text NOT NULL CHECK (peer_type IN ('user', 'channel')),
    peer_id bigint NOT NULL CHECK (peer_id > 0),
    peer_title text NOT NULL DEFAULT '' CHECK (octet_length(peer_title) <= 1024),
    peer_username text NOT NULL DEFAULT '' CHECK (octet_length(peer_username) <= 64),
    reason text NOT NULL DEFAULT '' CHECK (octet_length(reason) <= 16384),
    requested_description text NOT NULL DEFAULT '' CHECK (octet_length(requested_description) <= 280),
    status text NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'revoked')),
    decided_by text NOT NULL DEFAULT '' CHECK (octet_length(decided_by) <= 128),
    decision_reason text NOT NULL DEFAULT '' CHECK (octet_length(decision_reason) <= 4096),
    internal_note text NOT NULL DEFAULT '' CHECK (octet_length(internal_note) <= 32768),
    correlation_id text NOT NULL DEFAULT '' CHECK (octet_length(correlation_id) <= 128),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    approved_at timestamptz,
    rejected_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    CHECK (updated_at >= created_at),
    CHECK ((status = 'approved') = (approved_at IS NOT NULL)),
    CHECK ((status = 'rejected') = (rejected_at IS NOT NULL)),
    CHECK (status <> 'rejected' OR decision_reason <> '')
);

-- One live application per (verifier, peer): a second pending row would let two
-- decisions race for one mark.
CREATE UNIQUE INDEX custom_verification_requests_pending_idx
    ON public.custom_verification_requests (verifier_bot_id, peer_type, peer_id)
    WHERE status = 'pending';

CREATE INDEX custom_verification_requests_queue_idx
    ON public.custom_verification_requests (status, created_at DESC, id DESC);

CREATE INDEX custom_verification_requests_verifier_idx
    ON public.custom_verification_requests (verifier_bot_id, id DESC);

CREATE INDEX custom_verification_requests_applicant_idx
    ON public.custom_verification_requests (applicant_user_id, id DESC);

CREATE INDEX custom_verification_requests_peer_idx
    ON public.custom_verification_requests (peer_type, peer_id, id DESC);
