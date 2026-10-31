-- Channel direct messages (monoforum): the same sender (especially a channel
-- admin) may send messages with the same random_id to different subscriber
-- sub-conversations, so idempotency uniqueness must include saved_peer; otherwise
-- reusing a random_id across sub-conversations would be wrongly rejected by the
-- old three-column unique index or misjudged as a duplicate. For ordinary channel
-- messages saved_peer_id is constantly 0, so the new four-column tuple is
-- equivalent to the old (channel_id,sender_user_id,random_id) with no behavior change.
DROP INDEX IF EXISTS channel_messages_random_idx;
CREATE UNIQUE INDEX channel_messages_random_idx ON public.channel_messages
    USING btree (channel_id, sender_user_id, saved_peer_id, random_id) WHERE (random_id <> 0);
