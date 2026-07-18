-- This migration repairs invalid persisted capabilities and intentionally does
-- not restore them on downgrade: advertising Craft without an official crafted
-- model would reintroduce a user-visible operation that can never succeed.
SELECT 1;
