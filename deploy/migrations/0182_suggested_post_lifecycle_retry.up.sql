-- Renumbered after the Flashgram 0176-0177 migrations during upstream integration.
-- Isolate poisoned suggested-post lifecycle rows from the global due queue.
-- A failed aggregate remains durable and retryable, but receives a bounded
-- backoff so it cannot monopolize every one-second dispatcher pass.

ALTER TABLE public.suggested_post_approvals
    ADD COLUMN lifecycle_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN next_attempt_at integer NOT NULL DEFAULT 0,
    ADD COLUMN last_lifecycle_error text NOT NULL DEFAULT '';

ALTER TABLE public.suggested_post_approvals
    ADD CONSTRAINT suggested_post_approvals_retry_shape_check CHECK (
        lifecycle_attempts BETWEEN 0 AND 1000000 AND
        next_attempt_at >= 0 AND
        octet_length(last_lifecycle_error) <= 4096
    ) NOT VALID;

ALTER TABLE public.suggested_post_approvals
    VALIDATE CONSTRAINT suggested_post_approvals_retry_shape_check;

-- next_attempt_at is the queue's next eligible time, not merely a retry flag.
-- Healthy active rows point at their publish/settlement deadline; only an
-- explicit message-deletion wakeup or an already-due row is <= worker now.
UPDATE public.suggested_post_approvals
SET next_attempt_at = CASE state
    WHEN 'scheduled' THEN schedule_date
    WHEN 'published' THEN settlement_due
    ELSE 0
END
WHERE state IN ('scheduled','published');

CREATE INDEX suggested_post_approvals_retry_idx
    ON public.suggested_post_approvals(next_attempt_at,monoforum_id,suggestion_message_id)
    WHERE state IN ('scheduled','published');

CREATE INDEX suggested_post_approvals_published_message_idx
    ON public.suggested_post_approvals(parent_channel_id,published_message_id)
    INCLUDE (monoforum_id,suggestion_message_id,next_attempt_at)
    WHERE state='published';

-- A separate wakeup table avoids a message-row -> approval-row trigger lock
-- that would invert the worker's approval-row -> message-row order. It only
-- contains active suggested-post aggregates and is drained by atomic claim.
CREATE TABLE public.suggested_post_lifecycle_wakeups (
    monoforum_id bigint NOT NULL,
    suggestion_message_id integer NOT NULL,
    created_at integer NOT NULL,
    CONSTRAINT suggested_post_lifecycle_wakeups_pkey
        PRIMARY KEY (monoforum_id,suggestion_message_id),
    CONSTRAINT suggested_post_lifecycle_wakeups_shape_check
        CHECK (monoforum_id>0 AND suggestion_message_id>0 AND created_at>=0)
);

CREATE INDEX suggested_post_lifecycle_wakeups_due_idx
    ON public.suggested_post_lifecycle_wakeups(created_at,monoforum_id,suggestion_message_id);

-- Preserve deletions that predate this migration without making the recurring
-- worker rescan global channel tombstone history.
INSERT INTO public.suggested_post_lifecycle_wakeups(monoforum_id,suggestion_message_id,created_at)
SELECT a.monoforum_id,a.suggestion_message_id,0
FROM public.suggested_post_approvals a
WHERE (a.state='scheduled' AND EXISTS (
          SELECT 1 FROM public.channel_messages m
          WHERE m.channel_id=a.monoforum_id AND m.id=a.suggestion_message_id AND m.deleted))
   OR (a.state='published' AND EXISTS (
          SELECT 1 FROM public.channel_messages m
          WHERE m.channel_id=a.parent_channel_id AND m.id=a.published_message_id AND m.deleted))
ON CONFLICT DO NOTHING;

-- Deletion is a durable lifecycle wakeup. Drive the lookup from the exact
-- message being tombstoned and only insert a small queue fact; never rescan
-- global channel tombstones from the one-second worker.
CREATE OR REPLACE FUNCTION public.telesrv_wake_suggested_post_lifecycle_on_delete()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO public.suggested_post_lifecycle_wakeups(monoforum_id,suggestion_message_id,created_at)
    SELECT monoforum_id,suggestion_message_id,EXTRACT(EPOCH FROM clock_timestamp())::integer
    FROM public.suggested_post_approvals
    WHERE state='scheduled' AND monoforum_id=NEW.channel_id AND suggestion_message_id=NEW.id
    ON CONFLICT DO NOTHING;

    INSERT INTO public.suggested_post_lifecycle_wakeups(monoforum_id,suggestion_message_id,created_at)
    SELECT monoforum_id,suggestion_message_id,EXTRACT(EPOCH FROM clock_timestamp())::integer
    FROM public.suggested_post_approvals
    WHERE state='published' AND parent_channel_id=NEW.channel_id AND published_message_id=NEW.id
    ON CONFLICT DO NOTHING;
    RETURN NEW;
END
$$;

CREATE TRIGGER channel_messages_suggested_post_lifecycle_wakeup
AFTER UPDATE OF deleted ON public.channel_messages
FOR EACH ROW
WHEN (NEW.deleted AND NOT OLD.deleted)
EXECUTE FUNCTION public.telesrv_wake_suggested_post_lifecycle_on_delete();
