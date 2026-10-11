-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
-- The country-aware data rewrite is executed transactionally by
-- postgres.canonicalizeStoredPhoneIdentities before this durable version marker.
-- Keeping it in Go avoids incorrect SQL-only stripping of significant leading
-- zeroes (for example Italian fixed-line numbers).
SELECT 1;
