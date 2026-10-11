-- Add /deposit (the crypto donation deposit address from
-- 20260924140000_donations) to @premiumbot's command menu, same reasoning
-- as 20260922130000_premiumbot_claim_command: without this row the client's
-- "/" autocomplete never lists it even though the bot already answers it.

UPDATE public.bots
SET commands = '[
        {"command": "start", "description": "open the Premium storefront"},
        {"command": "premium", "description": "buy Premium for yourself"},
        {"command": "gift", "description": "gift Premium to someone"},
        {"command": "status", "description": "check your Premium status"},
        {"command": "history", "description": "show your purchase history"},
        {"command": "claim", "description": "claim your free monthly Stars"},
        {"command": "deposit", "description": "get your crypto deposit address"},
        {"command": "help", "description": "show help"}
    ]'::jsonb,
    updated_at = now()
WHERE bot_user_id = 1250000017;

UPDATE public.users
SET bot_info_version = bot_info_version + 1,
    updated_at = now()
WHERE id = 1250000017;
