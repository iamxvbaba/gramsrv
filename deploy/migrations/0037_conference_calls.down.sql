DROP TABLE IF EXISTS public.group_call_chain_blocks;
DROP TABLE IF EXISTS public.group_call_invites;

DROP INDEX IF EXISTS public.group_calls_conference_random_uniq;
DROP INDEX IF EXISTS public.group_calls_invite_slug_uniq;
DROP INDEX IF EXISTS public.group_calls_active_channel_uniq;

CREATE UNIQUE INDEX IF NOT EXISTS group_calls_active_channel_uniq
    ON public.group_calls USING btree (channel_id)
    WHERE (state = 'active'::text);

ALTER TABLE public.group_call_participants
    DROP COLUMN IF EXISTS join_block,
    DROP COLUMN IF EXISTS public_key;

ALTER TABLE public.group_calls
    DROP CONSTRAINT IF EXISTS group_calls_kind_check;

ALTER TABLE public.group_calls
    DROP COLUMN IF EXISTS migrated_from_phone_call_id,
    DROP COLUMN IF EXISTS random_id,
    DROP COLUMN IF EXISTS invite_link,
    DROP COLUMN IF EXISTS invite_slug,
    DROP COLUMN IF EXISTS kind;
