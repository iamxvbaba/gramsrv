\set ON_ERROR_STOP on

-- main and v2 intentionally have independent migration histories. Keep them
-- in separate PostgreSQL databases even when they share the local container.
SELECT 'CREATE DATABASE telesrv_main OWNER telesrv'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'telesrv_main')
\gexec

SELECT 'CREATE DATABASE telesrv_v2 OWNER telesrv'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'telesrv_v2')
\gexec
