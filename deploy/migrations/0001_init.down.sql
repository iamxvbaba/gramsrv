-- 0001_init rollback (no-op): this file is the initial schema of a greenfield
-- project, squashed from the 0001-0140 migration chain.
--
-- A greenfield project deliberately keeps no rollback path (the same convention as
-- the old 0011 migration). To reset the database, recreate it:
-- `docker compose -f deploy/docker-compose.yml down -v` destroys the volume and
-- re-migrates, rather than running golang-migrate down.
-- When golang-migrate executes this file it rolls the version back to 0 without
-- dropping any object or data.
SELECT 1;
