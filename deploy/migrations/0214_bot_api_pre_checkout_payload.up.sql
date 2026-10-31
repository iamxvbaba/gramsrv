-- pre_checkout_query is not tied to a message: the bot is asked before a payment
-- exists, so the update has neither a peer nor a message_id. It cannot be stored in
-- message columns; a separate payload is needed -- just as callback_query has its
-- own columns.
ALTER TABLE bot_api_updates
    ADD COLUMN IF NOT EXISTS pre_checkout_payload jsonb;
