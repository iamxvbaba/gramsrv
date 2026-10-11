-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
ALTER TABLE broadcasts
    DROP CONSTRAINT IF EXISTS broadcasts_entities_array_check,
    DROP COLUMN IF EXISTS entities;
