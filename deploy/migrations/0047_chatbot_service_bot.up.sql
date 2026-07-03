INSERT INTO public.users (
    id, access_hash, phone, first_name, last_name, username, country_code,
    created_at, updated_at, verified, support, about, last_seen_at,
    default_history_ttl_period, is_bot, bot_info_version, premium_expires_at,
    emoji_status_document_id, emoji_status_until, color_set, color,
    color_background_emoji_id, profile_color_set, profile_color,
    profile_color_background_emoji_id
) VALUES (
    1250000007, 6332902371644871201, '', 'ChatBot', '', 'ChatBot', '',
    now(), now(), true, false, 'Chat with the configured telesrv AI provider.',
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
    1250000007, 1250000007, '',
    'Chat with the configured telesrv AI provider.',
    '[
        {"command": "start", "description": "start chatting"},
        {"command": "help", "description": "show help"},
        {"command": "reset", "description": "clear local chat context"}
    ]'::jsonb,
    false, false, '', now(), now(), 0, '', '', false
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

INSERT INTO public.peer_usernames (username_lower, peer_type, peer_id, updated_at)
VALUES ('chatbot', 'user', 1250000007, now())
ON CONFLICT (username_lower) DO UPDATE SET
    peer_type = EXCLUDED.peer_type,
    peer_id = EXCLUDED.peer_id,
    updated_at = now();

INSERT INTO public.read_model_versions (model, owner_user_id, peer_type, peer_id, version, updated_at, hash)
VALUES
    ('contact_account', 1250000007, 'user', 1250000007, 1, now(), 2500000700001),
    ('channel_active_memberships', 1250000007, 'user', 1250000007, 1, now(), 2500000700002)
ON CONFLICT (model, owner_user_id, peer_type, peer_id) DO UPDATE SET
    version = GREATEST(public.read_model_versions.version, EXCLUDED.version),
    updated_at = now(),
    hash = EXCLUDED.hash;
