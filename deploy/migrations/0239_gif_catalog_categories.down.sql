ALTER TABLE public.gif_catalog
    DROP CONSTRAINT gif_catalog_file_name_valid,
    DROP CONSTRAINT gif_catalog_category_override_valid,
    DROP COLUMN category_override,
    DROP COLUMN file_name;
