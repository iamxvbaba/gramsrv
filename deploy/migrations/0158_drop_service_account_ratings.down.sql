-- Nothing to restore: the rating read model is derived from live signals, so a
-- deleted projection is recomputed rather than recovered, and the seeding pass
-- deliberately never offers these accounts again. Down is a no-op rather than a
-- lie about being able to bring the rows back.
SELECT 1;
