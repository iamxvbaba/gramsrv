-- Burning a collectible username must release the name for a fresh issue.
--
-- 0149 made collectible_usernames.username_lower globally UNIQUE, which kept a
-- burned asset's name reserved forever: the registry row was gone, so an
-- ordinary editable username could take it, but minting the collectible again
-- was impossible. Retiring an asset is a bookkeeping act on that asset, not a
-- permanent claim on the name, so uniqueness now covers only live assets.
--
-- Burned rows stay in place: they are the provenance record the transfer log
-- refers to, and several of them can accumulate for one name across reissues.

ALTER TABLE public.collectible_usernames
    DROP CONSTRAINT IF EXISTS collectible_usernames_username_lower_key;

CREATE UNIQUE INDEX collectible_usernames_live_name_idx
    ON public.collectible_usernames (username_lower)
    WHERE status <> 'burned';

CREATE INDEX collectible_usernames_name_history_idx
    ON public.collectible_usernames (username_lower, id DESC);
