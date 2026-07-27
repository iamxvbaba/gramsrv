-- Custom-emoji reactions on channel messages.
--
-- The squashed 0001_init schema constrains channel_message_reactions.reaction_type,
-- user_top_reactions.reaction_type and user_recent_reactions.reaction_type to
-- 'emoji' only, while the saved-tag tables already allow 'custom_emoji' and the
-- store writes reactionCustomEmoji through all of them. On a database built from
-- these migrations a custom-emoji reaction in a channel therefore fails with
-- "violates check constraint ..._type_check"; upstream's own
-- TestChannelStoreCustomEmojiReactionPolicyRoundTrips fails for exactly this
-- reason. Widen the three CHECKs to the pair the saved-tag tables already use.

ALTER TABLE public.channel_message_reactions
    DROP CONSTRAINT IF EXISTS channel_message_reactions_type_check;
ALTER TABLE public.channel_message_reactions
    ADD CONSTRAINT channel_message_reactions_type_check
    CHECK ((reaction_type)::text = ANY (ARRAY['emoji'::text, 'custom_emoji'::text]));

ALTER TABLE public.user_top_reactions
    DROP CONSTRAINT IF EXISTS user_top_reactions_reaction_type_check;
ALTER TABLE public.user_top_reactions
    ADD CONSTRAINT user_top_reactions_reaction_type_check
    CHECK ((reaction_type)::text = ANY (ARRAY['emoji'::text, 'custom_emoji'::text]));

ALTER TABLE public.user_recent_reactions
    DROP CONSTRAINT IF EXISTS user_recent_reactions_reaction_type_check;
ALTER TABLE public.user_recent_reactions
    ADD CONSTRAINT user_recent_reactions_reaction_type_check
    CHECK ((reaction_type)::text = ANY (ARRAY['emoji'::text, 'custom_emoji'::text]));
