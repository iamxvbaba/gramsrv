-- Placeholder migration (no-op): when gramsrv PR #74 was imported into v2 as is, it put
-- 0232_star_gift_limited_purchase_cooldown right after 0229, leaving 0230/0231 unused. golang-migrate records
-- only a single schema_migrations.version, and a database already at 0232 never runs a migration below 0232,
-- so the gap cannot take real DDL and is closed with no-ops: a database stopped at 0229 runs the two empty
-- migrations in order before 0232; a database already at 0232 skips them without missing any schema.
-- Later migrations from main are always appended after 0233, see docs/main-v2-sync.md.
SELECT 1;
