-- join_as：参与者可以以频道/群本身的身份入会（匿名管理员语义）。
-- 0 = 以本人用户身份；唯一键仍是 (call_id, user_id)，换身份 rejoin 为替换。
ALTER TABLE group_call_participants
    ADD COLUMN IF NOT EXISTS join_as_channel_id bigint DEFAULT 0 NOT NULL;
