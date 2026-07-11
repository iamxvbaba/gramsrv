-- 服务端已发送/构造的 update_states.pts 不是客户端确认：difference 响应可能在网络中
-- 丢失。observed_pts 只由客户端后续请求实际带回的 pts（或 getState 显式建立的快照
-- baseline）推进，retention 只能使用这个水位。
ALTER TABLE public.update_states
    ADD COLUMN observed_pts integer NOT NULL DEFAULT 0 CHECK (observed_pts >= 0);

-- 账号级 durable update 安全前缀回收水位。
-- 这里只记录“所有当前授权设备都已明确确认”的连续前缀；TDesktop 不支持
-- updates.differenceTooLong，故该表绝不能用于任意 TTL 硬裁剪。
CREATE TABLE public.user_update_retention (
    user_id bigint PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    retained_through_pts integer NOT NULL DEFAULT 0 CHECK (retained_through_pts >= 0),
    retained_through_date integer NOT NULL DEFAULT 0 CHECK (retained_through_date >= 0),
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);

CREATE INDEX user_update_retention_updated_idx
    ON public.user_update_retention (updated_at, user_id);

-- retention 先按时间挑全局最老的安全候选，再按 (user_id, pts) 删除连续前缀。
CREATE INDEX user_update_events_retention_global_idx
    ON public.user_update_events (date, user_id, pts);
