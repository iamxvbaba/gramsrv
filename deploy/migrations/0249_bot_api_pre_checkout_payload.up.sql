-- v2 number 0249; ported from main 0220_bot_api_pre_checkout_payload (main 3a84827a, B7b–7e), see docs/main-v2-sync.md.
-- pre_checkout_query is not tied to a message: the bot is asked before a payment
-- exists, so the update has neither a peer nor a message_id. It cannot be stored in
-- message columns; a separate payload is needed -- just as callback_query has its
-- own columns.
ALTER TABLE bot_api_updates
    ADD COLUMN IF NOT EXISTS pre_checkout_payload jsonb;
