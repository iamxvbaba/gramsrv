ALTER TABLE public.group_call_chain_blocks
    ADD COLUMN IF NOT EXISTS author_user_id bigint DEFAULT 0 NOT NULL;

CREATE INDEX IF NOT EXISTS group_call_chain_blocks_author_idx
    ON public.group_call_chain_blocks USING btree (call_id, sub_chain_id, author_user_id, block_offset);
