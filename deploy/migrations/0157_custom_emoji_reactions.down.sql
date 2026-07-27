-- Narrowing back can only succeed once no custom-emoji reaction rows remain, so
-- drop them first: a CHECK that a stored row violates cannot be added.
DELETE FROM public.channel_message_reactions WHERE (reaction_type)::text <> 'emoji';
DELETE FROM public.user_top_reactions WHERE (reaction_type)::text <> 'emoji';
DELETE FROM public.user_recent_reactions WHERE (reaction_type)::text <> 'emoji';

ALTER TABLE public.channel_message_reactions
    DROP CONSTRAINT IF EXISTS channel_message_reactions_type_check;
ALTER TABLE public.channel_message_reactions
    ADD CONSTRAINT channel_message_reactions_type_check
    CHECK ((reaction_type)::text = 'emoji'::text);

ALTER TABLE public.user_top_reactions
    DROP CONSTRAINT IF EXISTS user_top_reactions_reaction_type_check;
ALTER TABLE public.user_top_reactions
    ADD CONSTRAINT user_top_reactions_reaction_type_check
    CHECK ((reaction_type)::text = 'emoji'::text);

ALTER TABLE public.user_recent_reactions
    DROP CONSTRAINT IF EXISTS user_recent_reactions_reaction_type_check;
ALTER TABLE public.user_recent_reactions
    ADD CONSTRAINT user_recent_reactions_reaction_type_check
    CHECK ((reaction_type)::text = 'emoji'::text);
