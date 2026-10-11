-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
-- New-path channel reaction transactions outlive the bounded random_id command
-- receipts. Once any exists, an old binary could debit the payer again while
-- no longer crediting the channel. Never discard that cutover boundary; use a
-- pre-upgrade backup for rollback. Legacy backfill credits remain reversible
-- below because their exact transaction ids are retained in the audit table.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.channel_stars_transactions t
        WHERE t.reason='reaction'
          AND NOT EXISTS (
              SELECT 1
              FROM public.channel_paid_reaction_legacy_credits legacy
              WHERE legacy.channel_transaction_id=t.id
          )
    ) THEN
        RAISE EXCEPTION 'cannot downgrade paid reaction atomic ledger after post-upgrade activity; restore a pre-upgrade backup';
    END IF;
END $$;

ALTER TABLE public.channel_message_paid_reactions
    DROP CONSTRAINT IF EXISTS channel_message_paid_reactions_display_peer_check,
    DROP COLUMN IF EXISTS display_peer_id,
    DROP COLUMN IF EXISTS display_peer_type;

DROP TABLE IF EXISTS public.channel_paid_reaction_commands;
DROP TABLE IF EXISTS public.channel_paid_reaction_cutover;

-- A downgrade after later withdrawals may no longer have enough channel
-- balance to remove the upgrade credit. Fail closed instead of creating a
-- negative balance or deleting the audit trail.
DO $$
DECLARE
    legacy record;
    changed integer;
BEGIN
    FOR legacy IN
        SELECT channel_id,amount,channel_transaction_id
        FROM public.channel_paid_reaction_legacy_credits
        ORDER BY channel_id,reactor_user_id DESC
    LOOP
        UPDATE public.channel_stars_balances
        SET balance=balance-legacy.amount,updated_at=now()
        WHERE channel_id=legacy.channel_id AND balance>=legacy.amount;
        GET DIAGNOSTICS changed = ROW_COUNT;
        IF changed <> 1 THEN
            RAISE EXCEPTION 'cannot reverse paid reaction legacy credit for channel %', legacy.channel_id;
        END IF;
        DELETE FROM public.channel_stars_transactions WHERE id=legacy.channel_transaction_id;
    END LOOP;
END $$;

DROP TABLE IF EXISTS public.channel_paid_reaction_legacy_credits;
