ALTER TABLE public.webview_requested_buttons
    DROP COLUMN IF EXISTS photo_requested,
    DROP COLUMN IF EXISTS username_requested,
    DROP COLUMN IF EXISTS name_requested;
