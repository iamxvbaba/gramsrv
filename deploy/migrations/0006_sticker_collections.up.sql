-- Per-user personal sticker/GIF collections: faved stickers / recent stickers /
-- attach recent stickers / saved GIFs. Previously
-- messages.faveSticker/saveRecentSticker/saveGif were unregistered
-- (NOT_IMPLEMENTED) and getFaved/getRecent/getSavedGifs returned empty stubs.
-- This table provides real persistence.
-- used_at is the time of insertion/last use, and reads order by used_at DESC
-- (newest first); document_id references the documents table, and the read path
-- resolves it to a full document via Files.GetDocuments.
CREATE TABLE public.user_sticker_collections (
    owner_user_id bigint NOT NULL,
    kind text NOT NULL,
    document_id bigint NOT NULL,
    used_at integer DEFAULT 0 NOT NULL,
    CONSTRAINT user_sticker_collections_kind_check CHECK ((kind = ANY (ARRAY['faved'::text, 'recent'::text, 'recent_attached'::text, 'gif'::text])))
);

ALTER TABLE ONLY public.user_sticker_collections
    ADD CONSTRAINT user_sticker_collections_pkey PRIMARY KEY (owner_user_id, kind, document_id);

-- Reads/truncation by (owner, kind, used_at DESC).
CREATE INDEX user_sticker_collections_order_idx ON public.user_sticker_collections USING btree (owner_user_id, kind, used_at DESC);
