ALTER TABLE public.webview_requested_buttons
    ADD COLUMN name_requested boolean NOT NULL DEFAULT false,
    ADD COLUMN username_requested boolean NOT NULL DEFAULT false,
    ADD COLUMN photo_requested boolean NOT NULL DEFAULT false;
