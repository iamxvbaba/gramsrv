-- Per-scope notification settings (multiple rows per user): a specific peer /
-- forum topic / the three global defaults (users/chats/broadcasts). Previously
-- account.get/update/resetNotifySettings were echo stubs (not persisted), so a
-- mute was lost on restart and the dialog list's mute state was wrong. This table
-- provides real persistence.
-- Nullable bool/int columns mean "this item is unset = inherit the default of its
-- category" (consistent with TL flag-optional).
CREATE TABLE public.notify_settings (
    owner_user_id bigint NOT NULL,
    scope_kind text NOT NULL,
    peer_type text DEFAULT ''::text NOT NULL,
    peer_id bigint DEFAULT 0 NOT NULL,
    topic_id integer DEFAULT 0 NOT NULL,
    show_previews boolean,
    silent boolean,
    mute_until integer,
    stories_muted boolean,
    stories_hide_sender boolean,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT notify_settings_scope_kind_check CHECK ((scope_kind = ANY (ARRAY['peer'::text, 'users'::text, 'chats'::text, 'broadcasts'::text])))
);

ALTER TABLE ONLY public.notify_settings
    ADD CONSTRAINT notify_settings_pkey PRIMARY KEY (owner_user_id, scope_kind, peer_type, peer_id, topic_id);

-- Dialog-list bulk read: fetch whole-peer (topic 0) settings by owner + a set of peers.
CREATE INDEX notify_settings_owner_peer_idx ON public.notify_settings USING btree (owner_user_id, peer_type, peer_id) WHERE ((scope_kind = 'peer'::text) AND (topic_id = 0));
