-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
ALTER TABLE broadcasts
    ADD COLUMN entities jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE broadcasts
    ADD CONSTRAINT broadcasts_entities_array_check
    CHECK (jsonb_typeof(entities) = 'array');
