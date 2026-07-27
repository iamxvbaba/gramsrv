-- Service accounts carry no composite rating.
--
-- The rating measures what an account did with Stars, so a bot -- which does not
-- transact on its own behalf -- and the built-in service accounts -- which are
-- infrastructure rather than participants -- have no meaningful score. The seeding
-- pass now excludes both, but the platform account (777000) is not flagged is_bot,
-- so a deployment that already ran a recompute cycle has a projection row for it
-- and would show a level badge on the platform account's profile.
--
-- Drop those rows and their ledger. The read model is derived, so deleting a row
-- loses nothing that cannot be recomputed; the ledger rows go with it because a
-- manual adjustment for an account that can no longer be rated is unreachable
-- bookkeeping.
DELETE FROM public.account_rating_events
WHERE user_id IN (777000, 93372553, 1063110917, 1250000007, 1250000011, 1250000013)
   OR user_id IN (SELECT id FROM public.users WHERE is_bot);

DELETE FROM public.account_rating
WHERE user_id IN (777000, 93372553, 1063110917, 1250000007, 1250000011, 1250000013)
   OR user_id IN (SELECT id FROM public.users WHERE is_bot);
