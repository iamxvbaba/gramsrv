-- Channel direct messages (monoforum): direct messages are stored in
-- channel_messages (reusing channel pts/events/difference) and split into
-- sub-conversations per subscriber via the saved_peer dimension. For ordinary
-- channel messages saved_peer is empty, so the partial index covers only monoforum rows.
ALTER TABLE public.channel_messages
    ADD COLUMN saved_peer_type character varying(16) DEFAULT ''::character varying NOT NULL;
ALTER TABLE public.channel_messages
    ADD COLUMN saved_peer_id bigint DEFAULT 0 NOT NULL;
CREATE INDEX channel_messages_monoforum_sublist_idx ON public.channel_messages
    USING btree (channel_id, saved_peer_id, id DESC)
    WHERE ((saved_peer_id <> 0) AND (NOT deleted));
