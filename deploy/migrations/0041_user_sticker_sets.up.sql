CREATE TABLE IF NOT EXISTS public.user_sticker_sets (
    owner_user_id bigint NOT NULL,
    sticker_set_id bigint NOT NULL,
    set_kind text DEFAULT 'stickers'::text NOT NULL,
    archived boolean DEFAULT false NOT NULL,
    installed_date integer DEFAULT 0 NOT NULL,
    order_value bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_sticker_sets_pkey PRIMARY KEY (owner_user_id, sticker_set_id),
    CONSTRAINT user_sticker_sets_kind_check CHECK ((set_kind = ANY (ARRAY['stickers'::text, 'emoji'::text, 'masks'::text])))
);

CREATE INDEX IF NOT EXISTS user_sticker_sets_order_idx
    ON public.user_sticker_sets USING btree (owner_user_id, set_kind, archived, order_value DESC, sticker_set_id DESC);
