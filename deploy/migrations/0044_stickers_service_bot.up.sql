INSERT INTO public.users (
    id, access_hash, phone, first_name, last_name, username, country_code,
    created_at, updated_at, verified, support, about, last_seen_at,
    default_history_ttl_period, is_bot, bot_info_version, premium_expires_at,
    emoji_status_document_id, emoji_status_until, color_set, color,
    color_background_emoji_id, profile_color_set, profile_color,
    profile_color_background_emoji_id
) VALUES (
    1063110917, 5213187021149032991, '', 'Stickers', '', 'Stickers', '',
    now(), now(), true, false, 'Create custom sticker and emoji packs for telesrv.',
    0, 0, true, 2, NULL, 0, 0, false, 0, 0, false, 0, 0
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
    1063110917, 1063110917, '',
    'Create custom sticker and emoji packs for telesrv.',
    '[
        {"command": "start", "description": "start the sticker pack assistant"},
        {"command": "help", "description": "show help"},
        {"command": "newpack", "description": "create a sticker pack"},
        {"command": "newemoji", "description": "create a custom emoji pack"},
        {"command": "addsticker", "description": "add to one of your packs"},
        {"command": "delsticker", "description": "remove one item from a pack"},
        {"command": "publish", "description": "publish the current pack"},
        {"command": "cancel", "description": "cancel the current operation"},
        {"command": "packs", "description": "list your created packs"}
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
VALUES ('stickers', 'user', 1063110917, now())
ON CONFLICT (username_lower) DO UPDATE SET
    peer_type = EXCLUDED.peer_type,
    peer_id = EXCLUDED.peer_id,
    updated_at = now();

INSERT INTO public.read_model_versions (model, owner_user_id, peer_type, peer_id, version, updated_at, hash)
VALUES
    ('contact_account', 1063110917, 'user', 1063110917, 1, now(), 6110917000001),
    ('channel_active_memberships', 1063110917, 'user', 1063110917, 1, now(), 6110917000002)
ON CONFLICT (model, owner_user_id, peer_type, peer_id) DO UPDATE SET
    version = GREATEST(public.read_model_versions.version, EXCLUDED.version),
    updated_at = now(),
    hash = EXCLUDED.hash;
