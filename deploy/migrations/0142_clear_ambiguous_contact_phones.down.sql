-- Irreversible privacy cleanup: restoring users.phone here would recreate the
-- disclosure this migration removes. Contact relations and all non-phone
-- owner-scoped fields are preserved by the up migration.
SELECT 1;
