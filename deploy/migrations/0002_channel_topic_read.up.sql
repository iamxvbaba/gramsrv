-- Per-(user, channel, topic) independent read watermark.
-- Background: forum topic read-state was previously carried by the single
-- channel-level watermark channel_members.read_inbox_max_id, so reading one topic
-- polluted the computed unread of other topics (reply_to_top_id filter + id >
-- channel-level watermark). The read_*_max_id columns of channel_forum_topics have
-- no user dimension (PK = channel_id,topic_id) and cannot carry per-viewer read
-- state; hence this new table, where topic_id=1 means General.
CREATE TABLE public.channel_topic_read (
    channel_id bigint NOT NULL,
    user_id bigint NOT NULL,
    topic_id integer NOT NULL,
    read_inbox_max_id integer DEFAULT 0 NOT NULL,
    read_outbox_max_id integer DEFAULT 0 NOT NULL,
    read_inbox_date integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_topic_read_pkey PRIMARY KEY (channel_id, user_id, topic_id),
    CONSTRAINT channel_topic_read_topic_check CHECK (topic_id > 0),
    CONSTRAINT channel_topic_read_channel_fkey FOREIGN KEY (channel_id)
        REFERENCES public.channels (id) ON DELETE CASCADE
);

-- Outbox read-receipt reverse lookup: how far a viewer's own messages in a topic
-- have been read = the max read_inbox_max_id among the topic's other members.
-- (channel_id, topic_id, read_inbox_max_id DESC) lets MAX use an index-only scan,
-- avoiding a full-table aggregate per frame.
CREATE INDEX channel_topic_read_outbox_idx
    ON public.channel_topic_read (channel_id, topic_id, read_inbox_max_id DESC);
