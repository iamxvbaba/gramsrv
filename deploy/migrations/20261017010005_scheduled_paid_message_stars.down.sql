ALTER TABLE public.scheduled_messages DROP CONSTRAINT IF EXISTS scheduled_messages_paid_stars_check;
ALTER TABLE public.scheduled_messages DROP COLUMN IF EXISTS paid_stars;
