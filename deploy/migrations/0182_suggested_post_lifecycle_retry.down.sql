-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
DROP TRIGGER IF EXISTS channel_messages_suggested_post_lifecycle_wakeup ON public.channel_messages;
DROP FUNCTION IF EXISTS public.telesrv_wake_suggested_post_lifecycle_on_delete();

DROP TABLE IF EXISTS public.suggested_post_lifecycle_wakeups;
DROP INDEX IF EXISTS public.suggested_post_approvals_published_message_idx;
DROP INDEX IF EXISTS public.suggested_post_approvals_retry_idx;

ALTER TABLE public.suggested_post_approvals
    DROP CONSTRAINT IF EXISTS suggested_post_approvals_retry_shape_check,
    DROP COLUMN IF EXISTS last_lifecycle_error,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS lifecycle_attempts;
