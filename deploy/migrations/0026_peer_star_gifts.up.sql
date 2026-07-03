-- Star gifts are owned by a peer, not only by users: user gifts are addressed by
-- inputSavedStarGiftUser.msg_id, while channel gifts are addressed by
-- inputSavedStarGiftChat{peer,saved_id}.
ALTER TABLE public.user_star_gifts RENAME TO peer_star_gifts;
ALTER TABLE public.peer_star_gifts RENAME COLUMN owner_user_id TO owner_peer_id;

ALTER TABLE public.peer_star_gifts
    ADD COLUMN IF NOT EXISTS owner_peer_type text DEFAULT 'user' NOT NULL,
    ADD COLUMN IF NOT EXISTS saved_id bigint DEFAULT 0 NOT NULL;

ALTER TABLE public.peer_star_gifts
    DROP CONSTRAINT IF EXISTS user_star_gifts_owner_msg_uniq,
    DROP CONSTRAINT IF EXISTS peer_star_gifts_owner_peer_type_check,
    DROP CONSTRAINT IF EXISTS peer_star_gifts_ref_check;

DROP INDEX IF EXISTS public.user_star_gifts_owner_idx;
DROP INDEX IF EXISTS public.peer_star_gifts_owner_idx;
DROP INDEX IF EXISTS public.peer_star_gifts_user_msg_uniq;
DROP INDEX IF EXISTS public.peer_star_gifts_channel_saved_uniq;

ALTER TABLE public.peer_star_gifts
    ADD CONSTRAINT peer_star_gifts_owner_peer_type_check CHECK (owner_peer_type IN ('user', 'channel')),
    ADD CONSTRAINT peer_star_gifts_ref_check CHECK (
        (owner_peer_type = 'user' AND msg_id > 0 AND saved_id = 0)
        OR
        (owner_peer_type = 'channel' AND msg_id > 0 AND saved_id > 0)
    );

CREATE UNIQUE INDEX peer_star_gifts_user_msg_uniq
    ON public.peer_star_gifts (owner_peer_type, owner_peer_id, msg_id)
    WHERE owner_peer_type = 'user' AND msg_id > 0;

CREATE UNIQUE INDEX peer_star_gifts_channel_saved_uniq
    ON public.peer_star_gifts (owner_peer_type, owner_peer_id, saved_id)
    WHERE owner_peer_type = 'channel' AND saved_id > 0;

CREATE INDEX peer_star_gifts_owner_idx
    ON public.peer_star_gifts (owner_peer_type, owner_peer_id, gift_date DESC, id DESC);
