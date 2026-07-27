-- Built-in @verifierbot: the applicant front door for THIRD-PARTY verification
-- (core.telegram.org/api/bots/verification).
--
-- This is the first verifier bot of a deployment, shipped so the feature has a
-- working reference: it collects applications for its own icon+description mark
-- and reports the operator's decision back to the applicant. It is deliberately
-- NOT a second way to get the platform checkmark -- @verifybot (0153) owns that
-- mechanism, and the two never read each other's state.
--
-- verified = false on purpose. The official flag is granted by the operator to
-- peers that passed platform review; a verifier bot carrying it would blur exactly
-- the distinction this bot has to explain to every applicant. What makes this
-- account a verifier is a row in bot_verifier_settings (0155), which an operator
-- grants by hand in the admin panel -- never this migration: seeding verifier
-- status here would hand out a badge printer with the schema.
--
-- Seeded rather than created lazily on first message so the username is occupied
-- from the moment the schema is current: peer_usernames.username_lower is the only
-- thing standing between a reserved bot handle and an ordinary user claiming it,
-- and a lazily created account would leave that window open.
--
-- access_hash is double-written with domain.VerifierBotAccessHash; the two must
-- never drift, exactly as for the other service bots (0044, 0045, 0047, 0153).
--
-- The peer_usernames insert carries the multi-username registry columns added in
-- 0149, so the handle occupies the editable slot.

INSERT INTO public.users (
    id, access_hash, phone, first_name, last_name, username, country_code,
    created_at, updated_at, verified, support, about, last_seen_at,
    default_history_ttl_period, is_bot, bot_info_version, premium_expires_at,
    emoji_status_document_id, emoji_status_until, color_set, color,
    color_background_emoji_id, profile_color_set, profile_color,
    profile_color_background_emoji_id
) VALUES (
    1250000013, 6913402578811563729, '', 'Verifier Bot', '', 'verifierbot', '',
    now(), now(), false, false,
    'Third-party verification: my icon before your name and a line in your profile. Not the official checkmark.',
    0, 0, true, 1, NULL, 0, 0, false, 0, 0, false, 0, 0
)
ON CONFLICT (id) DO UPDATE SET
    access_hash = EXCLUDED.access_hash,
    phone = EXCLUDED.phone,
    first_name = EXCLUDED.first_name,
    last_name = EXCLUDED.last_name,
    username = EXCLUDED.username,
    verified = EXCLUDED.verified,
    support = EXCLUDED.support,
    about = EXCLUDED.about,
    is_bot = EXCLUDED.is_bot,
    bot_info_version = GREATEST(public.users.bot_info_version, EXCLUDED.bot_info_version),
    updated_at = now();

INSERT INTO public.bots (
    bot_user_id, owner_user_id, token_secret, description, commands,
    bot_chat_history, bot_nochats, inline_placeholder, created_at, updated_at,
    menu_button_type, menu_button_text, menu_button_url, bot_inline_geo
) VALUES (
    1250000013, 1250000013, '',
    'I grant third-party verification marks: my own icon before the name of your bot, channel or account, plus a description in its profile. This is not the official platform checkmark. I collect the application, an operator decides, and I message you here with the outcome.',
    '[
        {"command": "start", "description": "what a third-party mark is"},
        {"command": "verify", "description": "apply for the mark"},
        {"command": "status", "description": "your applications and marks"},
        {"command": "revoke", "description": "remove a mark from your peer"},
        {"command": "help", "description": "show help"}
    ]'::jsonb,
    false, true, '', now(), now(), 0, '', '', false
)
ON CONFLICT (bot_user_id) DO UPDATE SET
    owner_user_id = EXCLUDED.owner_user_id,
    token_secret = EXCLUDED.token_secret,
    description = EXCLUDED.description,
    commands = EXCLUDED.commands,
    bot_chat_history = EXCLUDED.bot_chat_history,
    bot_nochats = EXCLUDED.bot_nochats,
    inline_placeholder = EXCLUDED.inline_placeholder,
    menu_button_type = EXCLUDED.menu_button_type,
    menu_button_text = EXCLUDED.menu_button_text,
    menu_button_url = EXCLUDED.menu_button_url,
    bot_inline_geo = EXCLUDED.bot_inline_geo,
    updated_at = now();

INSERT INTO public.peer_usernames (
    username_lower, username, peer_type, peer_id, active, editable, sort_order, updated_at
)
VALUES ('verifierbot', 'verifierbot', 'user', 1250000013, true, true, 0, now())
ON CONFLICT (username_lower) DO UPDATE SET
    username = EXCLUDED.username,
    peer_type = EXCLUDED.peer_type,
    peer_id = EXCLUDED.peer_id,
    active = EXCLUDED.active,
    editable = EXCLUDED.editable,
    updated_at = now();

INSERT INTO public.read_model_versions (model, owner_user_id, peer_type, peer_id, version, updated_at, hash)
VALUES
    ('contact_account', 1250000013, 'user', 1250000013, 1, now(), 2500001300001),
    ('channel_active_memberships', 1250000013, 'user', 1250000013, 1, now(), 2500001300002)
ON CONFLICT (model, owner_user_id, peer_type, peer_id) DO UPDATE SET
    version = GREATEST(public.read_model_versions.version, EXCLUDED.version),
    updated_at = now(),
    hash = EXCLUDED.hash;
