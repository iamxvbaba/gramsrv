-- v2 编号 0254；移植自 main 0232_message_transcriptions（main 3a84827a，B7f），见 docs/main-v2-sync.md。
-- 每个 voice 文档一行转写结果；完成通知在同一事务写 edge_delivery_outbox（v2 不直推）。
CREATE TABLE message_transcriptions (
    document_id bigint NOT NULL PRIMARY KEY,
    transcription_id bigint NOT NULL,
    status text NOT NULL,
    text text NOT NULL DEFAULT '',
    requested_by bigint NOT NULL DEFAULT 0,
    peer_type text NOT NULL DEFAULT '',
    peer_id bigint NOT NULL DEFAULT 0,
    msg_id integer NOT NULL DEFAULT 0,
    failure text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT message_transcriptions_status_shape CHECK (status IN ('pending', 'done', 'failed'))
);
