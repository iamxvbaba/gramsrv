-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
-- system_key is the constructor-routing identity for global sticker sets. More
-- than one row makes messages.getStickerSet/inputStickerSet* nondeterministic
-- and can alternate clients between different catalogs after a restart.

-- StatusPack is now imported from the official export. Preserve the historical
-- synthesized set as an addressable short-name catalog, but only remove its
-- routing key when another real default-status set already owns that key.
UPDATE public.sticker_sets AS synthesized
SET system_key = '', updated_at = now()
WHERE synthesized.id = 7777000000000001
  AND synthesized.system_key = 'emoji_default_statuses'
  AND EXISTS (
      SELECT 1
      FROM public.sticker_sets AS real_set
      WHERE real_set.system_key = synthesized.system_key
        AND real_set.id <> synthesized.id
  );

DROP INDEX IF EXISTS public.sticker_sets_system_key_idx;

-- Fail closed on any other duplicate instead of making the read path choose an
-- arbitrary row. Empty keys are ordinary/non-system sets and are not unique.
CREATE UNIQUE INDEX sticker_sets_system_key_idx
  ON public.sticker_sets USING btree (system_key)
  WHERE system_key <> ''::text;
