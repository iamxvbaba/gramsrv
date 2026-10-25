-- v2 编号 0252；移植自 main 0223_star_gift_bot_not_convertible（main 3a84827a，B7b–7e），见 docs/main-v2-sync.md。
-- Bot API sendGift 送出的礼物不可转换回 Stars：转换资格以 peer_star_gifts.convert_stars
-- 为准，购买事务从 0217 起对机器人买家直接归零，本迁移把该修复回溯到修复之前
-- 已经送出、尚未转换的机器人礼物。发送人是机器人的判定看 users.is_bot（from_user_id
-- 恒为真实发送人，匿名只影响下发时的可见性，不影响这里的判定）。
-- 已转换（converted=true）的礼物不在此列：它们的历史转换已经结算，追回会改账，属
-- 于不可逆的数据变更，不做。
UPDATE public.peer_star_gifts gift
SET convert_stars = 0
WHERE NOT gift.converted
  AND gift.from_user_id > 0
  AND EXISTS (
      SELECT 1
      FROM public.users sender
      WHERE sender.id = gift.from_user_id
        AND sender.is_bot
  );