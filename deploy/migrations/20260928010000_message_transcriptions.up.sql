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
