ALTER TABLE public.webview_requested_buttons
    ADD COLUMN peer_filter jsonb NOT NULL DEFAULT '{}'::jsonb;
