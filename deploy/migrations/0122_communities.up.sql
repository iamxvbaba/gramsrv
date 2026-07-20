-- Layer 228 Communities are aggregation containers. Linked peer dialogs keep
-- their own messages/read state/pts; these tables only persist the container,
-- links, moderation, pending requests and per-user dialog presentation.
ALTER TABLE public.user_update_events
    DROP CONSTRAINT IF EXISTS user_update_events_peer_type_check;
ALTER TABLE public.user_update_events
    ADD CONSTRAINT user_update_events_peer_type_check
    CHECK (peer_type IS NULL OR peer_type IN ('user','channel','community'));

CREATE TABLE public.communities (
    id bigint PRIMARY KEY,
    access_hash bigint NOT NULL,
    creator_user_id bigint NOT NULL REFERENCES public.users(id),
    title text NOT NULL,
    about text DEFAULT ''::text NOT NULL,
    default_banned_rights jsonb DEFAULT '{}'::jsonb NOT NULL,
    photo_id bigint DEFAULT 0 NOT NULL,
    photo_dc_id integer DEFAULT 0 NOT NULL,
    photo_stripped bytea DEFAULT '\x'::bytea NOT NULL,
    date integer NOT NULL,
    deleted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT communities_positive_check CHECK (id > 0 AND creator_user_id > 0),
    CONSTRAINT communities_title_check CHECK (length(btrim(title)) > 0)
);

CREATE UNIQUE INDEX communities_access_hash_idx ON public.communities(access_hash);
CREATE INDEX communities_creator_idx ON public.communities(creator_user_id, id) WHERE NOT deleted;

CREATE TABLE public.community_members (
    community_id bigint NOT NULL REFERENCES public.communities(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES public.users(id),
    role text DEFAULT 'member'::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    admin_rights jsonb DEFAULT '{}'::jsonb NOT NULL,
    rank text DEFAULT ''::text NOT NULL,
    date integer NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (community_id, user_id),
    CONSTRAINT community_members_role_check CHECK (role IN ('creator','admin','member')),
    CONSTRAINT community_members_status_check CHECK (status IN ('active','kicked'))
);

CREATE UNIQUE INDEX community_one_creator_idx
    ON public.community_members(community_id) WHERE role = 'creator' AND status = 'active';
CREATE INDEX community_members_user_idx ON public.community_members(user_id, community_id) WHERE status = 'active';
CREATE INDEX community_members_kicked_idx ON public.community_members(community_id, user_id) WHERE status = 'kicked';

CREATE TABLE public.community_peer_links (
    community_id bigint NOT NULL REFERENCES public.communities(id) ON DELETE CASCADE,
    peer_type text NOT NULL,
    peer_id bigint NOT NULL,
    visibility text NOT NULL,
    created_by bigint NOT NULL REFERENCES public.users(id),
    date integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (community_id, peer_type, peer_id),
    CONSTRAINT community_peer_links_type_check CHECK (peer_type IN ('channel','user')),
    CONSTRAINT community_peer_links_visibility_check CHECK (visibility IN ('visible','hidden')),
    CONSTRAINT community_peer_links_positive_check CHECK (peer_id > 0 AND created_by > 0)
);

-- A group/channel/bot may belong to only one Community.
CREATE UNIQUE INDEX community_peer_links_unique_peer_idx ON public.community_peer_links(peer_type, peer_id);
CREATE INDEX community_peer_links_community_idx ON public.community_peer_links(community_id, date, peer_type, peer_id);

CREATE TABLE public.community_peer_link_requests (
    community_id bigint NOT NULL REFERENCES public.communities(id) ON DELETE CASCADE,
    peer_type text NOT NULL,
    peer_id bigint NOT NULL,
    requested_by bigint NOT NULL REFERENCES public.users(id),
    visibility text NOT NULL,
    date integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (community_id, peer_type, peer_id),
    CONSTRAINT community_peer_requests_type_check CHECK (peer_type IN ('channel','user')),
    CONSTRAINT community_peer_requests_visibility_check CHECK (visibility IN ('visible','hidden')),
    CONSTRAINT community_peer_requests_positive_check CHECK (peer_id > 0 AND requested_by > 0)
);

CREATE INDEX community_peer_requests_page_idx
    ON public.community_peer_link_requests(community_id, date DESC, peer_type, peer_id);

CREATE TABLE public.community_user_states (
    community_id bigint NOT NULL REFERENCES public.communities(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES public.users(id),
    collapsed boolean DEFAULT false NOT NULL,
    pinned boolean DEFAULT false NOT NULL,
    pinned_order integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (community_id, user_id),
    CONSTRAINT community_user_states_order_check CHECK (pinned_order >= 0)
);

CREATE INDEX community_user_states_pinned_idx
    ON public.community_user_states(user_id, pinned_order, community_id) WHERE pinned;

ALTER TABLE public.users
    ADD COLUMN linked_community_id bigint DEFAULT 0 NOT NULL;
ALTER TABLE public.channels
    ADD COLUMN linked_community_id bigint DEFAULT 0 NOT NULL;

CREATE INDEX users_linked_community_idx ON public.users(linked_community_id) WHERE linked_community_id <> 0;
CREATE INDEX channels_linked_community_idx ON public.channels(linked_community_id) WHERE linked_community_id <> 0;

-- linked_community_id is part of the Layer 228 user projection. The legacy
-- user-base trigger intentionally lists projected columns and therefore does
-- not notice this newly-added column; emit the same read-model bumps here so
-- Redis/base-user and contact projections cannot retain a stale bot link.
CREATE FUNCTION public.telesrv_notify_user_linked_community_read_model() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM telesrv_bump_read_model_version('user_base', NEW.id, 'user', NEW.id);
    PERFORM telesrv_bump_read_model_version('contact_account', c.user_id, 'user', c.user_id)
    FROM contacts c
    WHERE c.contact_user_id = NEW.id;
    PERFORM telesrv_bump_private_dialog_light_for_user(NEW.id);
    RETURN NULL;
END;
$$;

CREATE TRIGGER users_linked_community_read_model_changed
AFTER UPDATE OF linked_community_id ON public.users
FOR EACH ROW
WHEN (OLD.linked_community_id IS DISTINCT FROM NEW.linked_community_id)
EXECUTE FUNCTION public.telesrv_notify_user_linked_community_read_model();

-- Community notification settings reuse the existing peer-scoped table with a
-- distinct peer_type. No new scope_kind is required.
