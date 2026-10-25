-- Fact-check verdicts are published to every subscriber of the channel; dropping
-- the column would silently retract published verdicts. This migration is
-- forward-only.
DO $$ BEGIN RAISE EXCEPTION '0264 (main 0240) preserves published fact-check verdicts; roll forward instead'; END $$;
