-- Export the versions stored in PostgreSQL before synchronizing data/langpack.
-- In psql, redirect the result to a file:
--   \copy (SELECT lang_pack, lang_code, version FROM lang_packs
--          ORDER BY lang_pack, lang_code) TO 'db_langpack_versions.csv' CSV HEADER
SELECT lang_pack, lang_code, version
FROM public.lang_packs
ORDER BY lang_pack, lang_code;
