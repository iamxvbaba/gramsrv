CREATE INDEX CONCURRENTLY IF NOT EXISTS channel_messages_live_pinned_idx
    ON public.channel_messages (channel_id, id DESC)
    WHERE pinned AND NOT deleted;
