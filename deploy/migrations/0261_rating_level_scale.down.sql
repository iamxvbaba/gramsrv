-- Nothing to restore: the level ladder is a server-side policy curve, not stored
-- state. Re-deriving the 50-level thresholds from the same scores is lossy (every
-- score would have to be rounded back down), and the read model is recomputed from
-- live signals on the next cycle anyway, so down is a no-op rather than a
-- plausible-looking approximation of the old curve.
SELECT 1;
