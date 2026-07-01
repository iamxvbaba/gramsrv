UPDATE public.bots
SET commands = '[
        {"command": "start", "description": "start the sticker pack assistant"},
        {"command": "help", "description": "show help"},
        {"command": "newpack", "description": "create a sticker pack"},
        {"command": "newemoji", "description": "create a custom emoji pack"},
        {"command": "publish", "description": "publish the current pack"},
        {"command": "cancel", "description": "cancel the current operation"},
        {"command": "packs", "description": "list your created packs"}
    ]'::jsonb,
    updated_at = now()
WHERE bot_user_id = 1063110917;
