-- Terminal failed rows are short-lived diagnostic quarantine entries.  Cleanup starts from the
-- one-row-per-user durable head so it can lock in the same head→outbox order as claim/completion
-- without scanning healthy lanes.
CREATE INDEX dispatch_outbox_user_heads_failed_cleanup_idx
    ON public.dispatch_outbox_user_heads (
        updated_at,
        target_user_id,
        head_id
    )
    WHERE status = 'failed';
