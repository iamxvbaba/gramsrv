-- Deployments already at migration 0211 skipped the colliding 0209/0210
-- login-code and GIF migrations. Apply their schema changes at the next slot.
ALTER TABLE public.login_code_message_deliveries
    ADD COLUMN IF NOT EXISTS template text NOT NULL DEFAULT '';

COMMENT ON COLUMN public.login_code_message_deliveries.template IS
    'Rendered first-delivery template with {{code}} placeholder, never the secret code; empty means legacy built-in template';

ALTER TABLE public.gif_catalog
    ADD COLUMN IF NOT EXISTS file_name text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS category_override text;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.gif_catalog'::regclass AND conname = 'gif_catalog_file_name_valid') THEN
        ALTER TABLE public.gif_catalog ADD CONSTRAINT gif_catalog_file_name_valid CHECK (char_length(file_name) <= 255);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.gif_catalog'::regclass AND conname = 'gif_catalog_category_override_valid') THEN
        ALTER TABLE public.gif_catalog ADD CONSTRAINT gif_catalog_category_override_valid CHECK (
            category_override IS NULL OR category_override IN
            ('reaction', 'humor', 'animals', 'sports', 'celebration', 'other')
        );
    END IF;
END $$;

UPDATE public.gif_catalog
SET file_name = source_filename
WHERE file_name = '' AND source_filename <> '';
