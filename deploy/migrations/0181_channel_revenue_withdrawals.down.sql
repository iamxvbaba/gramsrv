-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
-- A completed claim moved value into a creator's personal ledger and its
-- receipt is the only exact rollback evidence. Never discard that evidence or
-- let 0178 subtract legacy revenue after a payout; restore from backup instead.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.channel_revenue_withdrawals
        WHERE status='completed'
    ) THEN
        RAISE EXCEPTION 'cannot downgrade channel revenue withdrawals after a completed claim; restore a pre-upgrade backup';
    END IF;
END $$;

DROP TABLE IF EXISTS public.channel_revenue_withdrawals;
