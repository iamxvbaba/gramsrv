DROP TABLE IF EXISTS public.saved_message_reaction_tags;

ALTER TABLE public.user_saved_reaction_tags
    DROP CONSTRAINT IF EXISTS user_saved_reaction_tags_reaction_type_check;
DELETE FROM public.user_saved_reaction_tags
WHERE (reaction_type)::text <> 'emoji'::text;
ALTER TABLE public.user_saved_reaction_tags
    ADD CONSTRAINT user_saved_reaction_tags_reaction_type_check
    CHECK ((reaction_type)::text = 'emoji'::text);
