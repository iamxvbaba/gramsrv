DROP INDEX IF EXISTS public.group_call_chain_blocks_author_idx;

ALTER TABLE public.group_call_chain_blocks
    DROP COLUMN IF EXISTS author_user_id;
