-- Scheduled video chat: group_calls gains schedule_date (>0 = scheduled, not yet
-- started); start-notification subscriptions (the per-user state of
-- toggleGroupCallStartSubscription).
ALTER TABLE group_calls
    ADD COLUMN IF NOT EXISTS schedule_date integer DEFAULT 0 NOT NULL;

CREATE TABLE IF NOT EXISTS group_call_schedule_subscribers (
    call_id bigint NOT NULL,
    user_id bigint NOT NULL,
    PRIMARY KEY (call_id, user_id)
);
