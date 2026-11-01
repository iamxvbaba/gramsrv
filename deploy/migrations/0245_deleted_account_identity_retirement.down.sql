-- 0218 is irreversible: assets went to the vault with a provenance-journal entry,
-- and the retired username rows and profile photos lost their original flag values
-- and sort_order projections. Restoring would mean resurrecting either an asset or a
-- "taken" name for a deleted account, which directly contradicts the migration's
-- goal. As with 0217, the rollback is refused rather than inventing values to patch
-- the data.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.users
        WHERE deleted_at IS NOT NULL
    ) AND EXISTS (
        SELECT 1
        FROM public.collectible_username_transfers
        WHERE actor = 'telesrv:account-deletion'
    ) THEN
        RAISE EXCEPTION 'cannot roll back 0218: deleted-account identities were retired and their prior state was not kept';
    END IF;
END
$$;