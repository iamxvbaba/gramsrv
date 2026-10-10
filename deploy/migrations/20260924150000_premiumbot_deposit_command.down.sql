UPDATE public.bots
SET commands = '[
        {"command": "start", "description": "open the Premium storefront"},
        {"command": "premium", "description": "buy Premium for yourself"},
        {"command": "gift", "description": "gift Premium to someone"},
        {"command": "status", "description": "check your Premium status"},
        {"command": "history", "description": "show your purchase history"},
        {"command": "claim", "description": "claim your free monthly Stars"},
        {"command": "help", "description": "show help"}
    ]'::jsonb,
    updated_at = now()
WHERE bot_user_id = 1250000017;
