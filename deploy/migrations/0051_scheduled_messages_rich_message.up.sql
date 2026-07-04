ALTER TABLE public.scheduled_messages
    ADD COLUMN IF NOT EXISTS rich_message jsonb DEFAULT '{}'::jsonb NOT NULL;
