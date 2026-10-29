-- v2 编号 0268；移植自 main 0246_scheduled_paid_message_stars（main 815612a9，gramsrv#152，B21），见 docs/main-v2-sync.md。
ALTER TABLE public.scheduled_messages DROP CONSTRAINT IF EXISTS scheduled_messages_paid_stars_check;
ALTER TABLE public.scheduled_messages DROP COLUMN IF EXISTS paid_stars;
