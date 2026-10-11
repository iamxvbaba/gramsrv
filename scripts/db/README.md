# Database checks and recovery helpers

These files are operator-run, read-only checks or narrowly scoped recovery
helpers. They are not part of the automatic migration stream. Always take a
backup and inspect the output before running a write utility.

```sh
psql "$TELESRV_POSTGRES_DSN" -f scripts/db/check_schema.sql
psql "$TELESRV_POSTGRES_DSN" -f scripts/db/check_animation.sql
psql "$TELESRV_POSTGRES_DSN" -f scripts/db/get_langpack_versions.sql \
  > db_langpack_versions.csv
```

`migrate_bot_verifier_legacy.sql` copies rows from the old 0155 verifier shape
when both the renamed legacy table and the current table already exist. If a
database still has the old table under its original name, first rename it and
apply `deploy/migrations/0155_bot_verification.up.sql`; the helper refuses to
guess at that destructive transition.

After exporting language-pack versions, preview and then apply file renames:

```powershell
./scripts/rename_langpack_files.ps1 -VersionsCsv ./db_langpack_versions.csv -WhatIf
./scripts/rename_langpack_files.ps1 -VersionsCsv ./db_langpack_versions.csv
```

The current schema already contains the message-entity migration
`0174_star_gift_message_entities`; `check_entities.sql` verifies its columns
and constraints without duplicating that migration.
