BEGIN;
ALTER TABLE numbers ADD COLUMN IF NOT EXISTS retired BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE numbers DROP CONSTRAINT IF EXISTS numbers_retired_current_check;
ALTER TABLE numbers ADD CONSTRAINT numbers_retired_current_check CHECK (NOT retired OR NOT is_current);
COMMIT;
