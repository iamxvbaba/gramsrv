-- Custom cloud themes (account.createTheme/uploadTheme, etc.). themes = the catalog
-- of user-created themes; document_id softly references documents (no hard foreign
-- key, to avoid coupling with the media table; a theme may outlive its blob, in
-- which case getTheme degrades to no document). settings = JSONB of
-- []domain.ThemeSettingsSpec (non-empty only for accent themes).
-- theme_user_installs = each user's installed/saved theme list.
CREATE TABLE public.themes (
    id bigint NOT NULL,
    access_hash bigint DEFAULT 0 NOT NULL,
    creator_user_id bigint NOT NULL,
    slug text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    emoticon text DEFAULT ''::text NOT NULL,
    for_chat boolean DEFAULT false NOT NULL,
    document_id bigint DEFAULT 0 NOT NULL,
    settings jsonb DEFAULT '[]'::jsonb NOT NULL,
    installs_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT themes_pkey PRIMARY KEY (id),
    CONSTRAINT themes_slug_key UNIQUE (slug),
    CONSTRAINT themes_creator_user_id_fkey FOREIGN KEY (creator_user_id)
        REFERENCES public.users(id) ON DELETE CASCADE
);

CREATE INDEX themes_creator_user_id_idx ON public.themes (creator_user_id);

CREATE TABLE public.theme_user_installs (
    user_id bigint NOT NULL,
    theme_id bigint NOT NULL,
    dark boolean DEFAULT false NOT NULL,
    installed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT theme_user_installs_pkey PRIMARY KEY (user_id, theme_id),
    CONSTRAINT theme_user_installs_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES public.users(id) ON DELETE CASCADE,
    CONSTRAINT theme_user_installs_theme_id_fkey FOREIGN KEY (theme_id)
        REFERENCES public.themes(id) ON DELETE CASCADE
);

CREATE INDEX theme_user_installs_user_id_idx ON public.theme_user_installs (user_id);
