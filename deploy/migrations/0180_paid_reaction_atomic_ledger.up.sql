-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
-- Durable idempotency command and first-settlement receipt for
-- messages.sendPaidReaction.
CREATE TABLE public.channel_paid_reaction_commands (
    payer_user_id bigint NOT NULL,
    random_id bigint NOT NULL,
    request_fingerprint bytea NOT NULL,
    channel_id bigint NOT NULL,
    message_id integer NOT NULL,
    stars bigint NOT NULL,
    anonymous boolean NOT NULL,
    reaction_date integer NOT NULL,
    completed boolean DEFAULT false NOT NULL,
    payer_transaction_id bigint,
    channel_transaction_id bigint,
    payer_balance_after bigint,
    channel_balance_after bigint,
    reactor_stars_after bigint,
    total_stars_after bigint,
    result_snapshot bytea,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_paid_reaction_commands_pkey PRIMARY KEY (payer_user_id, random_id),
    CONSTRAINT channel_paid_reaction_commands_payer_txn_fkey
        FOREIGN KEY (payer_transaction_id) REFERENCES public.stars_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT channel_paid_reaction_commands_channel_txn_fkey
        FOREIGN KEY (channel_transaction_id) REFERENCES public.channel_stars_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT channel_paid_reaction_commands_shape_check CHECK (
        payer_user_id > 0 AND random_id <> 0 AND octet_length(request_fingerprint) = 32 AND
        channel_id > 0 AND message_id > 0 AND stars > 0 AND reaction_date > 0
    ),
    CONSTRAINT channel_paid_reaction_commands_receipt_check CHECK (
        (NOT completed AND payer_transaction_id IS NULL AND channel_transaction_id IS NULL AND
            payer_balance_after IS NULL AND channel_balance_after IS NULL AND
            reactor_stars_after IS NULL AND total_stars_after IS NULL AND result_snapshot IS NULL)
        OR
        (completed AND payer_transaction_id IS NOT NULL AND channel_transaction_id IS NOT NULL AND
            payer_balance_after >= 0 AND channel_balance_after >= 0 AND
            reactor_stars_after > 0 AND total_stars_after > 0 AND octet_length(result_snapshot) > 0)
    ),
    CONSTRAINT channel_paid_reaction_commands_payer_txn_unique UNIQUE (payer_transaction_id),
    CONSTRAINT channel_paid_reaction_commands_channel_txn_unique UNIQUE (channel_transaction_id)
);

CREATE INDEX channel_paid_reaction_commands_channel_idx
    ON public.channel_paid_reaction_commands(channel_id, message_id, payer_user_id);
CREATE INDEX channel_paid_reaction_commands_retention_idx
    ON public.channel_paid_reaction_commands(created_at);

-- Old writers persisted a successful aggregate but never the wire random_id.
-- If one response was lost at cutover, accepting that identifier again would
-- debit twice. With old writers stopped, every legacy command timestamp is at
-- most cutover plus the accepted five-minute client clock skew. Fence that
-- interval only when legacy aggregates actually exist; a fresh installation
-- has no rollout brownout.
CREATE TABLE public.channel_paid_reaction_cutover (
    singleton boolean PRIMARY KEY DEFAULT true,
    cutover_at integer NOT NULL,
    reject_random_id_through integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_paid_reaction_cutover_singleton_check CHECK (singleton),
    CONSTRAINT channel_paid_reaction_cutover_shape_check CHECK (
        cutover_at > 0 AND
        (reject_random_id_through = 0 OR reject_random_id_through = cutover_at + 300)
    )
);

INSERT INTO public.channel_paid_reaction_cutover(singleton,cutover_at,reject_random_id_through)
SELECT true,cutover_at,
       CASE WHEN EXISTS (SELECT 1 FROM public.channel_message_paid_reactions)
            THEN cutover_at + 300 ELSE 0 END
FROM (SELECT EXTRACT(EPOCH FROM clock_timestamp())::integer AS cutover_at) boundary;

-- Leaderboard identity is distinct from the payer: paidReactionPrivacyPeer
-- represents an owned broadcast channel. Historical rows displayed the payer
-- user, so that exact already-observed identity is preserved during upgrade.
ALTER TABLE public.channel_message_paid_reactions
    ADD COLUMN display_peer_type text DEFAULT 'user' NOT NULL,
    ADD COLUMN display_peer_id bigint DEFAULT 0 NOT NULL;

UPDATE public.channel_message_paid_reactions
SET display_peer_id = reactor_user_id
WHERE display_peer_id = 0;

ALTER TABLE public.channel_message_paid_reactions
    ADD CONSTRAINT channel_message_paid_reactions_display_peer_check CHECK (
        display_peer_type IN ('user','channel') AND display_peer_id > 0
    );

-- Before 0178, RPC debited the payer and only then wrote this aggregate; a
-- store failure rolled the aggregate back and attempted a payer refund. There
-- was no channel reaction credit path. Therefore every persisted aggregate is
-- an authoritative successful paid reaction and is credited exactly once on
-- upgrade. Group by (channel,reactor) to keep an auditable payer attribution;
-- do not synthesize random_id receipts for history that never stored them.
CREATE TABLE public.channel_paid_reaction_legacy_credits (
    channel_id bigint NOT NULL,
    reactor_user_id bigint NOT NULL,
    amount bigint NOT NULL,
    reaction_rows integer NOT NULL,
    reaction_date integer NOT NULL,
    channel_balance_after bigint NOT NULL,
    channel_transaction_id bigint NOT NULL,
    migrated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_paid_reaction_legacy_credits_pkey PRIMARY KEY (channel_id,reactor_user_id),
    CONSTRAINT channel_paid_reaction_legacy_credits_txn_unique UNIQUE (channel_transaction_id),
    CONSTRAINT channel_paid_reaction_legacy_credits_txn_fkey
        FOREIGN KEY (channel_transaction_id) REFERENCES public.channel_stars_transactions(id) ON DELETE CASCADE,
    CONSTRAINT channel_paid_reaction_legacy_credits_shape_check CHECK (
        channel_id>0 AND reactor_user_id>0 AND amount>0 AND reaction_rows>0 AND
        reaction_date>0 AND channel_balance_after>=amount
    )
);

DO $$
DECLARE
    legacy record;
    balance_after bigint;
    transaction_id bigint;
BEGIN
    FOR legacy IN
        SELECT channel_id,reactor_user_id,SUM(stars)::bigint AS amount,
               COUNT(*)::integer AS reaction_rows,GREATEST(MAX(reaction_date),1)::integer AS reaction_date
        FROM public.channel_message_paid_reactions
        GROUP BY channel_id,reactor_user_id
        ORDER BY channel_id,reactor_user_id
    LOOP
        INSERT INTO public.channel_stars_balances(channel_id,balance)
        VALUES(legacy.channel_id,legacy.amount)
        ON CONFLICT(channel_id) DO UPDATE
        SET balance=public.channel_stars_balances.balance+EXCLUDED.balance,updated_at=now()
        RETURNING balance INTO balance_after;

        INSERT INTO public.channel_stars_transactions
            (channel_id,actor_user_id,amount,reason,peer_type,peer_id,date)
        VALUES
            (legacy.channel_id,legacy.reactor_user_id,legacy.amount,'reaction',
             'user',legacy.reactor_user_id,legacy.reaction_date)
        RETURNING id INTO transaction_id;

        INSERT INTO public.channel_paid_reaction_legacy_credits
            (channel_id,reactor_user_id,amount,reaction_rows,reaction_date,
             channel_balance_after,channel_transaction_id)
        VALUES
            (legacy.channel_id,legacy.reactor_user_id,legacy.amount,legacy.reaction_rows,
             legacy.reaction_date,balance_after,transaction_id);
    END LOOP;
END $$;
