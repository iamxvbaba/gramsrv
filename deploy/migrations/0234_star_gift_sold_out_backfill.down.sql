-- Irreversible data backfill: unsetting sold_out would re-hide the sale
-- timestamps the flag gates, so there is no safe inverse.
SELECT 1;