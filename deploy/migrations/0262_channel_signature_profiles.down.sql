-- Preserve profile-signature history and message privacy; this migration is forward-only.
DO $$ BEGIN RAISE EXCEPTION '0262 (main 0238) preserves sender visibility snapshots; roll forward instead'; END $$;
