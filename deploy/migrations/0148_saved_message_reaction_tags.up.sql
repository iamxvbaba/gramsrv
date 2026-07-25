CREATE TABLE public.saved_message_reaction_tags (
    user_id bigint NOT NULL,
    message_box_id integer NOT NULL,
    reaction_type character varying(16) NOT NULL,
    reaction_value text NOT NULL,
    chosen_order integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT saved_message_reaction_tags_pkey
        PRIMARY KEY (user_id, message_box_id, reaction_type, reaction_value),
    CONSTRAINT saved_message_reaction_tags_order_check CHECK (chosen_order > 0),
    CONSTRAINT saved_message_reaction_tags_type_check
        CHECK ((reaction_type)::text = ANY (ARRAY['emoji'::text, 'custom_emoji'::text])),
    CONSTRAINT saved_message_reaction_tags_value_check CHECK (reaction_value <> ''),
    CONSTRAINT saved_message_reaction_tags_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    CONSTRAINT saved_message_reaction_tags_message_box_fkey
        FOREIGN KEY (user_id, message_box_id)
        REFERENCES public.message_boxes(owner_user_id, box_id) ON DELETE CASCADE
);

CREATE INDEX saved_message_reaction_tags_reaction_message_idx
    ON public.saved_message_reaction_tags
    (user_id, ((reaction_type)::text || ':' || reaction_value), message_box_id DESC);

ALTER TABLE public.user_saved_reaction_tags
    DROP CONSTRAINT user_saved_reaction_tags_reaction_type_check;
ALTER TABLE public.user_saved_reaction_tags
    ADD CONSTRAINT user_saved_reaction_tags_reaction_type_check
    CHECK ((reaction_type)::text = ANY (ARRAY['emoji'::text, 'custom_emoji'::text]));

COMMENT ON COLUMN public.user_saved_reaction_tags.reaction_count IS
    'Legacy unused column; visible counts are aggregated from saved_message_reaction_tags.';
