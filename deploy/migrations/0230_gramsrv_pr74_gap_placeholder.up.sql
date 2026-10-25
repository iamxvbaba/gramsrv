-- 占位迁移（no-op）：gramsrv PR #74 原样导入 v2 时把 0232_star_gift_limited_purchase_cooldown
-- 直接接在 0229 之后，留下 0230/0231 空号。golang-migrate 只记录单个 schema_migrations.version，
-- 已升到 0232 的库永远不会再执行低于 0232 的迁移，因此空号不能补真实 DDL，只能用 no-op 闭合：
-- 停在 0229 的库顺序执行两个空迁移后再到 0232；已在 0232 的库跳过它们也不缺任何 schema。
-- 来自 main 的后续迁移一律追加在 0233 之后，见 docs/main-v2-sync.md。
SELECT 1;
