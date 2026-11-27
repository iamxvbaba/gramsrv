-- Atomic per-channel message id allocation.
--
-- The previous allocator derived the next id from MAX(id) outside the write
-- transaction, so two concurrent writers (a registration auto-subscribe join
-- racing a message send, or two sends) picked the same id and the loser hit
-- channel_messages_pkey. A per-channel counter row serializes allocation and
-- keeps the id space monotonic independently of the message table snapshot.
CREATE TABLE IF NOT EXISTS public.channel_message_id_counters (
    channel_id bigint PRIMARY KEY,
    next_id bigint NOT NULL
);

INSERT INTO public.channel_message_id_counters (channel_id, next_id)
SELECT channel_id, COALESCE(MAX(id), 0)
FROM public.channel_messages
GROUP BY channel_id
ON CONFLICT (channel_id) DO NOTHING;
