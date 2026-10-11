-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
DROP INDEX IF EXISTS public.sticker_sets_system_key_idx;

CREATE INDEX sticker_sets_system_key_idx
  ON public.sticker_sets USING btree (system_key)
  WHERE system_key <> ''::text;
