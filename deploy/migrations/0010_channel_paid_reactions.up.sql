-- Channel post paid reaction (messages.sendPaidReaction): each reactor
-- accumulates the number of Stars they put in on a channel message, plus whether
-- they are anonymous. Previously sendPaidReaction always returned
-- BALANCE_TOO_LOW (no ledger, no accumulation). It works with the Stars ledger
-- (migration 0009): the RPC first Debits from the ledger, then accumulates here;
-- the message shows the ReactionPaid total Star count and the top-reactors
-- ranking. Star counts are positive and accumulate by (channel,message,user).
CREATE TABLE public.channel_message_paid_reactions (
    channel_id bigint NOT NULL,
    message_id integer NOT NULL,
    reactor_user_id bigint NOT NULL,
    stars bigint DEFAULT 0 NOT NULL,
    anonymous boolean DEFAULT false NOT NULL,
    reaction_date integer DEFAULT 0 NOT NULL,
    CONSTRAINT channel_message_paid_reactions_pkey PRIMARY KEY (channel_id, message_id, reactor_user_id),
    CONSTRAINT channel_message_paid_reactions_stars_pos CHECK ((stars > 0))
);

-- The top-reactors ranking is by (channel, message, stars DESC).
CREATE INDEX channel_message_paid_reactions_top_idx ON public.channel_message_paid_reactions USING btree (channel_id, message_id, stars DESC);
