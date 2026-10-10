-- pre_checkout_query не привязан к сообщению: бота спрашивают до появления платежа,
-- поэтому у апдейта нет ни peer, ни message_id. Хранить его в колонках сообщений
-- нельзя, нужен отдельный payload - так же, как у callback_query свои колонки.
ALTER TABLE bot_api_updates
    ADD COLUMN IF NOT EXISTS pre_checkout_payload jsonb;
