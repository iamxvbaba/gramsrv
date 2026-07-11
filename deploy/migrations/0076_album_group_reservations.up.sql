-- sendMultiMedia 必须在解析上传媒体、逐条发送之前持久预留 grouped_id。
-- 这张表把每个发送 random_id 固定到会话作用域内的相册组，使中途失败后
-- 客户端只重试失败子集时仍能恢复首次整包使用的 grouped_id；intent_hash
-- 同时阻止内容已改变的相同 random_id 借旧预留错误合并。
CREATE TABLE album_group_reservations (
    sender_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    peer_type TEXT NOT NULL CHECK (peer_type IN ('user', 'channel')),
    peer_id BIGINT NOT NULL CHECK (peer_id > 0),
    random_id BIGINT NOT NULL CHECK (random_id <> 0),
    intent_hash BYTEA NOT NULL CHECK (octet_length(intent_hash) = 32),
    grouped_id BIGINT NOT NULL CHECK (grouped_id <> 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (sender_user_id, peer_type, peer_id, random_id)
);

COMMENT ON TABLE album_group_reservations IS
    'Durable pre-send binding from album item random_id to grouped_id; never reconstructed from a retry subset.';
