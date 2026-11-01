-- v2 number 0267; ported from main 0245_private_paid_message_stars (main 815612a9, gramsrv#152，B21), see docs/main-v2-sync.md.
ALTER TABLE public.message_boxes DROP CONSTRAINT IF EXISTS message_boxes_paid_message_stars_check;
ALTER TABLE public.message_boxes DROP COLUMN IF EXISTS paid_message_stars;
ALTER TABLE public.private_messages DROP CONSTRAINT IF EXISTS private_messages_paid_message_stars_check;
ALTER TABLE public.private_messages DROP COLUMN IF EXISTS paid_message_stars;
