-- 迁移把已存在行上的 convert_stars 归零，原始值没有副本；向下恢复会凭空把不是
-- 机器人送的礼物也标记成可转换，或读不出来原始数据。拒绝这种不可逆回滚。
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.peer_star_gifts gift
        JOIN public.users sender ON sender.id = gift.from_user_id
        WHERE NOT gift.converted
          AND sender.is_bot
          AND gift.convert_stars = 0
    ) THEN
        RAISE EXCEPTION 'cannot roll back 0217: bot-sent gifts are already non-convertible and the original values were dropped';
    END IF;
END
$$;