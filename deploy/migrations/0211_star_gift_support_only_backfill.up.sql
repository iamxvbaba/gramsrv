-- golang-migrate only moves forward: deployments already past the original
-- 0204 slot never saw support_only. This end-slot backfill closes the gap.
ALTER TABLE public.star_gift_catalog_revisions
    ADD COLUMN IF NOT EXISTS support_only boolean DEFAULT false NOT NULL;
