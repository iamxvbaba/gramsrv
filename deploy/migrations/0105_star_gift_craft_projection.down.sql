ALTER TABLE public.star_gift_craft_commands
    DROP CONSTRAINT IF EXISTS star_gift_craft_source_edit_shape_check,
    DROP COLUMN IF EXISTS source_edit_pts;

-- Burned/crafted lifecycle facts and emitted edit events are authoritative
-- business history. Downgrade intentionally does not resurrect consumed gifts.
SELECT 1;
