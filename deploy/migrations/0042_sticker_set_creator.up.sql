ALTER TABLE public.sticker_sets
  ADD COLUMN IF NOT EXISTS creator_user_id bigint DEFAULT 0 NOT NULL,
  ADD COLUMN IF NOT EXISTS text_color boolean DEFAULT false NOT NULL,
  ADD COLUMN IF NOT EXISTS deleted boolean DEFAULT false NOT NULL,
  ADD COLUMN IF NOT EXISTS software text DEFAULT ''::text NOT NULL,
  ADD COLUMN IF NOT EXISTS keywords jsonb DEFAULT '[]'::jsonb NOT NULL,
  ADD COLUMN IF NOT EXISTS updated_at timestamp with time zone DEFAULT now() NOT NULL;

DROP INDEX IF EXISTS public.sticker_sets_short_name_idx;

CREATE UNIQUE INDEX IF NOT EXISTS sticker_sets_short_name_lower_idx
  ON public.sticker_sets USING btree (lower(short_name))
  WHERE (short_name <> ''::text AND deleted = false);

CREATE INDEX IF NOT EXISTS sticker_sets_creator_idx
  ON public.sticker_sets USING btree (creator_user_id, id DESC)
  WHERE (creator_user_id <> 0 AND deleted = false);
