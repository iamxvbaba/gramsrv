-- This backfill repairs deployments with different histories. A generic down
-- migration could remove columns that another migration created; intentionally
-- keep the data and schema intact.
SELECT 1;
