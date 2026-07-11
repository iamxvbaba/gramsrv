-- upload.saveFilePart data is transient, while messages.sendMedia may be replayed after its first
-- response is lost. Preserve the immutable Photo/Document materialization selected for each
-- (owner,file_id) so InputMediaUploaded* remains idempotent after part cleanup.
CREATE TABLE public.uploaded_media_receipts (
    owner_user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    file_id bigint NOT NULL CHECK (file_id <> 0),
    intent_hash bytea NOT NULL CHECK (octet_length(intent_hash) = 32),
    media_kind text NOT NULL CHECK (media_kind IN ('photo', 'document')),
    media_id bigint NOT NULL CHECK (media_id <> 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_user_id, file_id)
);
