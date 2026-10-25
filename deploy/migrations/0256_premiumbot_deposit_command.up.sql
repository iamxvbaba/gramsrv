-- v2 编号 0256；改写自 main 0229_premiumbot_deposit_command（main 3a84827a，B7g），见 docs/main-v2-sync.md。
-- 给内置 @premiumbot 的命令菜单追加 /deposit（加密货币充值地址），客户端 "/" 补全才会列出。
--
-- v2 改写：main 的 0229 写的是 bot 1250000017，而该 ID 在 main 与 v2 中都是 @gifbot；
-- @premiumbot 是 1250000015。main 还整份覆盖命令列表并带上 v2 没有的 /claim。
-- 这里只在默认 @premiumbot 行上追加缺失的 /deposit，保留现有命令与顺序。

UPDATE public.bots
SET commands = commands || '[{"command": "deposit", "description": "получить адрес для крипто-депозита"}]'::jsonb,
    updated_at = now()
WHERE bot_user_id = 1250000015
  AND jsonb_typeof(commands) = 'array'
  AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(commands) AS c WHERE c->>'command' = 'deposit');

UPDATE public.users
SET bot_info_version = bot_info_version + 1,
    updated_at = now()
WHERE id = 1250000015;
