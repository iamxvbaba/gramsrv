CREATE TABLE public.chatlist_invites (
    id bigserial PRIMARY KEY,
    owner_user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    filter_id integer NOT NULL CHECK (filter_id >= 2),
    slug text NOT NULL UNIQUE CHECK (slug <> ''),
    title text NOT NULL DEFAULT '',
    peers jsonb NOT NULL DEFAULT '[]'::jsonb,
    revoked boolean NOT NULL DEFAULT false,
    deleted boolean NOT NULL DEFAULT false,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_chatlist_invites_owner_filter_slug
    ON public.chatlist_invites(owner_user_id, filter_id, slug);

CREATE INDEX idx_chatlist_invites_owner_filter_live
    ON public.chatlist_invites(owner_user_id, filter_id, created_at, slug)
    WHERE NOT deleted;

CREATE TABLE public.chatlist_memberships (
    user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    local_filter_id integer NOT NULL CHECK (local_filter_id >= 2),
    owner_user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    owner_filter_id integer NOT NULL CHECK (owner_filter_id >= 2),
    slug text NOT NULL REFERENCES public.chatlist_invites(slug),
    hidden_updates boolean NOT NULL DEFAULT false,
    joined_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, local_filter_id),
    UNIQUE (user_id, slug)
);

CREATE INDEX idx_chatlist_memberships_user_slug
    ON public.chatlist_memberships(user_id, slug);

CREATE INDEX idx_chatlist_memberships_slug
    ON public.chatlist_memberships(slug);
