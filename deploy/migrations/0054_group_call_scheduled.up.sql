-- Scheduled video chat：group_calls 增加 schedule_date（>0=定时未开始）；
-- 开播提醒订阅（toggleGroupCallStartSubscription 的 per-user 状态）。
ALTER TABLE group_calls
    ADD COLUMN IF NOT EXISTS schedule_date integer DEFAULT 0 NOT NULL;

CREATE TABLE IF NOT EXISTS group_call_schedule_subscribers (
    call_id bigint NOT NULL,
    user_id bigint NOT NULL,
    PRIMARY KEY (call_id, user_id)
);
