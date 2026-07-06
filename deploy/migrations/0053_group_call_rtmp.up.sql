-- Live Stream（RTMP 直播）：group_calls 增加 rtmp_stream 标记；
-- per-channel 持久 RTMP 推流密钥（revoke 轮换后旧 key 立即失效）。
ALTER TABLE group_calls
    ADD COLUMN IF NOT EXISTS rtmp_stream boolean DEFAULT false NOT NULL;

CREATE TABLE IF NOT EXISTS group_call_rtmp_keys (
    channel_id bigint PRIMARY KEY,
    stream_key text NOT NULL,
    updated_at integer NOT NULL
);
