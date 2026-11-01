-- v2 number 0254; ported from main 0232_message_transcriptions (main 3a84827a, B7f), see docs/main-v2-sync.md.
-- One transcription result row per voice document; the completion notice is written to
-- edge_delivery_outbox in the same transaction (v2 does not push directly).
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
