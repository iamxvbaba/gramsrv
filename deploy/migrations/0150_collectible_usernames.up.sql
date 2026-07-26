-- Collectible (Fragment-style) usernames.
--
-- A peer keeps exactly one editable username -- the slot that
-- account.updateUsername / channels.updateUsername owns -- plus any number of
-- collectible usernames it holds. peer_usernames stays the single authoritative
-- registry for global uniqueness across users and channels, so ResolveUsername,
-- public landing pages and occupancy checks keep working for collectible names
-- without a second lookup path.
--
-- The pre-existing one-row-per-peer unique index is replaced by a partial index
-- covering only editable rows: the old invariant is preserved exactly for the
-- editable slot while collectible rows are free to accumulate.
--
-- Ownership lives in collectible_usernames (the asset) and is projected into
-- peer_usernames (the registry). A collectible row in the registry always
-- carries collectible_id and is never editable, so client-driven username edits
-- cannot mutate or release an owned asset.

CREATE TABLE public.collectible_usernames (
    id bigserial PRIMARY KEY,
    username text NOT NULL,
    username_lower text NOT NULL UNIQUE CHECK (
        username_lower <> '' AND lower(username) = username_lower
    ),
    status text NOT NULL CHECK (status IN ('vault', 'owned', 'burned')),
    -- Empty owner is the vault/burned state; 'owned' always has a real peer.
    owner_peer_type text NOT NULL CHECK (owner_peer_type IN ('', 'user', 'channel')),
    owner_peer_id bigint NOT NULL CHECK (owner_peer_id >= 0),
    CHECK (
        (status = 'owned' AND owner_peer_type <> '' AND owner_peer_id > 0)
        OR (status <> 'owned' AND owner_peer_type = '' AND owner_peer_id = 0)
    ),
    -- fragment.collectibleInfo projection. Amounts are minor units for fiat and
    -- nanotons for TON, matching the star gift lifecycle ledger convention.
    purchase_date timestamptz NOT NULL,
    currency text NOT NULL CHECK (currency IN ('XTR', 'TON', 'USD')),
    amount bigint NOT NULL CHECK (amount >= 0),
    crypto_currency text NOT NULL DEFAULT '' CHECK (crypto_currency IN ('', 'TON')),
    crypto_amount bigint NOT NULL DEFAULT 0 CHECK (crypto_amount >= 0),
    CHECK (
        (crypto_currency = '' AND crypto_amount = 0)
        OR (crypto_currency <> '' AND crypto_amount > 0)
    ),
    url text NOT NULL DEFAULT '' CHECK (octet_length(url) <= 512),
    -- Provenance: the first holder, kept even after transfers and burns.
    original_owner_peer_type text NOT NULL DEFAULT '' CHECK (
        original_owner_peer_type IN ('', 'user', 'channel')
    ),
    original_owner_peer_id bigint NOT NULL DEFAULT 0 CHECK (original_owner_peer_id >= 0),
    transfer_count integer NOT NULL DEFAULT 0 CHECK (transfer_count >= 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (updated_at >= created_at)
);

CREATE INDEX collectible_usernames_owner_idx
    ON public.collectible_usernames (owner_peer_type, owner_peer_id, id DESC)
    WHERE status = 'owned';

CREATE INDEX collectible_usernames_status_idx
    ON public.collectible_usernames (status, id DESC);

-- Append-only provenance log. command_key makes admin mint/transfer/revoke
-- replay-safe the same way star_gift_admin_grant_commands does for gifts.
CREATE TABLE public.collectible_username_transfers (
    id bigserial PRIMARY KEY,
    collectible_id bigint NOT NULL REFERENCES public.collectible_usernames(id)
        ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('mint', 'transfer', 'revoke', 'burn')),
    from_peer_type text NOT NULL CHECK (from_peer_type IN ('', 'user', 'channel')),
    from_peer_id bigint NOT NULL CHECK (from_peer_id >= 0),
    to_peer_type text NOT NULL CHECK (to_peer_type IN ('', 'user', 'channel')),
    to_peer_id bigint NOT NULL CHECK (to_peer_id >= 0),
    currency text NOT NULL DEFAULT '' CHECK (currency IN ('', 'XTR', 'TON', 'USD')),
    amount bigint NOT NULL DEFAULT 0 CHECK (amount >= 0),
    actor text NOT NULL DEFAULT '' CHECK (octet_length(actor) <= 128),
    reason text NOT NULL DEFAULT '' CHECK (octet_length(reason) <= 512),
    command_key text CHECK (command_key IS NULL OR octet_length(command_key) BETWEEN 1 AND 128),
    created_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX collectible_username_transfers_command_idx
    ON public.collectible_username_transfers (command_key)
    WHERE command_key IS NOT NULL;

CREATE INDEX collectible_username_transfers_asset_idx
    ON public.collectible_username_transfers (collectible_id, id DESC);

ALTER TABLE public.peer_usernames
    ADD COLUMN username text NOT NULL DEFAULT '',
    ADD COLUMN active boolean NOT NULL DEFAULT true,
    ADD COLUMN editable boolean NOT NULL DEFAULT true,
    ADD COLUMN sort_order integer NOT NULL DEFAULT 0,
    ADD COLUMN collectible_id bigint REFERENCES public.collectible_usernames(id)
        ON DELETE CASCADE;

-- Recover the original-case display form for rows written before the column
-- existed; fall back to the lowercase key when the peer row is already gone.
UPDATE public.peer_usernames pu
SET username = u.username
FROM public.users u
WHERE pu.peer_type = 'user'
  AND pu.peer_id = u.id
  AND pu.username = ''
  AND lower(u.username) = pu.username_lower;

UPDATE public.peer_usernames pu
SET username = c.username
FROM public.channels c
WHERE pu.peer_type = 'channel'
  AND pu.peer_id = c.id
  AND pu.username = ''
  AND lower(COALESCE(c.username, '')) = pu.username_lower;

UPDATE public.peer_usernames
SET username = username_lower
WHERE username = '';

ALTER TABLE public.peer_usernames
    ALTER COLUMN username DROP DEFAULT,
    ADD CONSTRAINT peer_usernames_username_case_check
        CHECK (lower(username) = username_lower),
    ADD CONSTRAINT peer_usernames_collectible_not_editable_check
        CHECK (collectible_id IS NULL OR NOT editable),
    ADD CONSTRAINT peer_usernames_sort_order_check
        CHECK (sort_order >= 0 AND sort_order <= 1024);

DROP INDEX IF EXISTS public.peer_usernames_peer_unique_idx;

CREATE UNIQUE INDEX peer_usernames_peer_editable_idx
    ON public.peer_usernames (peer_type, peer_id)
    WHERE editable;

CREATE INDEX peer_usernames_peer_order_idx
    ON public.peer_usernames (peer_type, peer_id, sort_order, username_lower);

CREATE UNIQUE INDEX peer_usernames_collectible_idx
    ON public.peer_usernames (collectible_id)
    WHERE collectible_id IS NOT NULL;

-- Peer deletion must not destroy a collectible asset: the registry row goes
-- away with the peer, the asset returns to the vault and keeps its provenance.
CREATE OR REPLACE FUNCTION public.delete_user_peer_username() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE public.collectible_usernames
    SET status = 'vault',
        owner_peer_type = '',
        owner_peer_id = 0,
        version = version + 1,
        updated_at = now()
    WHERE status = 'owned'
      AND owner_peer_type = 'user'
      AND owner_peer_id = OLD.id;

    DELETE FROM public.peer_usernames
    WHERE peer_type = 'user' AND peer_id = OLD.id;

    RETURN OLD;
END;
$$;

CREATE OR REPLACE FUNCTION public.delete_channel_peer_username() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE public.collectible_usernames
    SET status = 'vault',
        owner_peer_type = '',
        owner_peer_id = 0,
        version = version + 1,
        updated_at = now()
    WHERE status = 'owned'
      AND owner_peer_type = 'channel'
      AND owner_peer_id = OLD.id;

    DELETE FROM public.peer_usernames
    WHERE peer_type = 'channel' AND peer_id = OLD.id;

    RETURN OLD;
END;
$$;
