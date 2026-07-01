DROP INDEX IF EXISTS public.sticker_sets_creator_idx;
DROP INDEX IF EXISTS public.sticker_sets_short_name_lower_idx;

CREATE UNIQUE INDEX IF NOT EXISTS sticker_sets_short_name_idx
  ON public.sticker_sets USING btree (short_name)
  WHERE (short_name <> ''::text);

ALTER TABLE public.sticker_sets
  DROP COLUMN IF EXISTS updated_at,
  DROP COLUMN IF EXISTS keywords,
  DROP COLUMN IF EXISTS software,
  DROP COLUMN IF EXISTS deleted,
  DROP COLUMN IF EXISTS text_color,
  DROP COLUMN IF EXISTS creator_user_id;
