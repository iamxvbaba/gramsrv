ALTER TABLE public.group_calls
    ADD COLUMN IF NOT EXISTS kind text DEFAULT 'channel'::text NOT NULL,
    ADD COLUMN IF NOT EXISTS invite_slug text DEFAULT ''::text NOT NULL,
    ADD COLUMN IF NOT EXISTS invite_link text DEFAULT ''::text NOT NULL,
    ADD COLUMN IF NOT EXISTS random_id bigint DEFAULT 0 NOT NULL,
    ADD COLUMN IF NOT EXISTS migrated_from_phone_call_id bigint DEFAULT 0 NOT NULL;

ALTER TABLE public.group_calls
    DROP CONSTRAINT IF EXISTS group_calls_kind_check;

ALTER TABLE public.group_calls
    ADD CONSTRAINT group_calls_kind_check CHECK (kind = ANY (ARRAY['channel'::text, 'conference'::text]));

ALTER TABLE public.group_call_participants
    ADD COLUMN IF NOT EXISTS public_key bytea,
    ADD COLUMN IF NOT EXISTS join_block bytea;

DROP INDEX IF EXISTS public.group_calls_active_channel_uniq;

CREATE UNIQUE INDEX IF NOT EXISTS group_calls_active_channel_uniq
    ON public.group_calls USING btree (channel_id)
    WHERE ((state = 'active'::text) AND (channel_id <> 0));

CREATE UNIQUE INDEX IF NOT EXISTS group_calls_invite_slug_uniq
    ON public.group_calls USING btree (invite_slug)
    WHERE (invite_slug <> ''::text);

CREATE UNIQUE INDEX IF NOT EXISTS group_calls_conference_random_uniq
    ON public.group_calls USING btree (creator_user_id, random_id)
    WHERE ((kind = 'conference'::text) AND (random_id <> 0));

CREATE TABLE IF NOT EXISTS public.group_call_invites (
    call_id bigint NOT NULL REFERENCES public.group_calls(call_id) ON DELETE CASCADE,
    inviter_user_id bigint NOT NULL,
    invitee_user_id bigint NOT NULL,
    message_id integer NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    video boolean DEFAULT false NOT NULL,
    created_at integer NOT NULL,
    updated_at integer DEFAULT 0 NOT NULL,
    CONSTRAINT group_call_invites_status_check CHECK (status = ANY (ARRAY['pending'::text, 'accepted'::text, 'declined'::text, 'missed'::text, 'revoked'::text])),
    CONSTRAINT group_call_invites_pkey PRIMARY KEY (call_id, invitee_user_id, message_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS group_call_invites_msg_uniq
    ON public.group_call_invites USING btree (invitee_user_id, message_id);

CREATE INDEX IF NOT EXISTS group_call_invites_call_idx
    ON public.group_call_invites USING btree (call_id, invitee_user_id);

CREATE TABLE IF NOT EXISTS public.group_call_chain_blocks (
    call_id bigint NOT NULL REFERENCES public.group_calls(call_id) ON DELETE CASCADE,
    sub_chain_id integer DEFAULT 0 NOT NULL,
    block_offset integer NOT NULL,
    block bytea NOT NULL,
    created_at integer NOT NULL,
    CONSTRAINT group_call_chain_blocks_pkey PRIMARY KEY (call_id, sub_chain_id, block_offset)
);
