-- Live Stream (RTMP live): group_calls gains an rtmp_stream flag;
-- a per-channel persistent RTMP stream key (after a revoke rotation the old key is
-- invalid immediately).
ALTER TABLE group_calls
    ADD COLUMN IF NOT EXISTS rtmp_stream boolean DEFAULT false NOT NULL;

CREATE TABLE IF NOT EXISTS group_call_rtmp_keys (
    channel_id bigint PRIMARY KEY,
    stream_key text NOT NULL,
    updated_at integer NOT NULL
);
