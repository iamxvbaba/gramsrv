-- The wallet gram ledger moved between two accounts needs an idempotency key:
-- a client that retries after a timeout must not move the money twice. The
-- ledger itself (ton_balances / ton_transactions) has no place for a key, so
-- the key and its result live in their own table.
--
-- The key is unique across every kind of wallet gram command, so a key that
-- was used for a transfer can never be reused for another movement.
CREATE TABLE IF NOT EXISTS wallet_gram_commands (
    id                bigserial PRIMARY KEY,
    command_key       text NOT NULL,
    kind              text NOT NULL DEFAULT 'transfer',
    from_user_id      bigint NOT NULL DEFAULT 0,
    to_user_id        bigint NOT NULL DEFAULT 0,
    amount_nanoton    bigint NOT NULL DEFAULT 0,
    balance_after     bigint NOT NULL DEFAULT 0,
    transaction_id    bigint NOT NULL DEFAULT 0,
    created_at        timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT wallet_gram_commands_key_unique UNIQUE (command_key),
    CONSTRAINT wallet_gram_commands_kind_check CHECK (kind IN ('transfer', 'topup'))
);

CREATE INDEX IF NOT EXISTS wallet_gram_commands_created_idx ON wallet_gram_commands (created_at DESC);
CREATE INDEX IF NOT EXISTS wallet_gram_commands_to_user_idx ON wallet_gram_commands (to_user_id, id DESC);
CREATE INDEX IF NOT EXISTS wallet_gram_commands_from_user_idx ON wallet_gram_commands (from_user_id, id DESC);

-- The comment of a wallet movement: the note the sender writes on a transfer.
--
-- The ledger kept the reason and the counterparty but not the note, so a
-- transfer arrived on the other side as a bare number. The client's wallet
-- history has a field for it (wallet.getTransactions), and it is what makes a
-- transfer readable months later.
ALTER TABLE public.ton_transactions
    ADD COLUMN IF NOT EXISTS comment text NOT NULL DEFAULT '';
