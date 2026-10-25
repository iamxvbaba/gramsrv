-- v2 编号 0249；移植自 main 0220_bot_api_pre_checkout_payload（main 3a84827a，B7b–7e），见 docs/main-v2-sync.md。
-- pre_checkout_query не привязан к сообщению: бота спрашивают до появления платежа,
-- поэтому у апдейта нет ни peer, ни message_id. Хранить его в колонках сообщений
-- нельзя, нужен отдельный payload - так же, как у callback_query свои колонки.
ALTER TABLE bot_api_updates
    ADD COLUMN IF NOT EXISTS pre_checkout_payload jsonb;
