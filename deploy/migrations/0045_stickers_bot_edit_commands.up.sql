UPDATE public.users
SET bot_info_version = GREATEST(bot_info_version, 2),
    updated_at = now()
WHERE id = 1063110917;

UPDATE public.bots
SET commands = '[
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
    updated_at = now()
WHERE bot_user_id = 1063110917;
