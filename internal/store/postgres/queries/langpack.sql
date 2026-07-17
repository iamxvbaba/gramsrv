-- name: GetLangPackMeta :one
SELECT lang_pack, lang_code, version, strings_count
FROM lang_packs
WHERE lang_pack = $1 AND lang_code = $2;

-- name: UpsertLangPackMeta :exec
INSERT INTO lang_packs (lang_pack, lang_code, version, strings_count)
VALUES ($1, $2, $3, $4)
ON CONFLICT (lang_pack, lang_code) DO UPDATE SET
  version = EXCLUDED.version,
  strings_count = EXCLUDED.strings_count,
  updated_at = now();

-- name: UpsertLangPackString :exec
INSERT INTO lang_pack_strings (
  lang_pack, lang_code, key, version, pluralized, value,
  zero_value, one_value, two_value, few_value, many_value, other_value, deleted
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (lang_pack, lang_code, key) DO UPDATE SET
  version = EXCLUDED.version,
  pluralized = EXCLUDED.pluralized,
  value = EXCLUDED.value,
  zero_value = EXCLUDED.zero_value,
  one_value = EXCLUDED.one_value,
  two_value = EXCLUDED.two_value,
  few_value = EXCLUDED.few_value,
  many_value = EXCLUDED.many_value,
  other_value = EXCLUDED.other_value,
  deleted = EXCLUDED.deleted,
  updated_at = now();

-- name: ListLangPackCodes :many
SELECT lang_code
FROM lang_packs
WHERE lang_pack = $1
ORDER BY lang_code;

-- name: DeleteLangPackStrings :exec
DELETE FROM lang_pack_strings
WHERE lang_pack = $1 AND lang_code = $2;

-- name: DeleteLangPackMeta :exec
DELETE FROM lang_packs
WHERE lang_pack = $1 AND lang_code = $2;

-- name: GetLangPackSeedHash :one
SELECT content_hash
FROM seed_states
WHERE key = $1;

-- name: PutLangPackSeedHash :exec
INSERT INTO seed_states (key, content_hash)
VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET
  content_hash = EXCLUDED.content_hash,
  updated_at = now();

-- name: DeleteLangPackSeedHash :exec
DELETE FROM seed_states
WHERE key = $1;

-- name: ListLangPackStrings :many
SELECT
  lang_pack, lang_code, key, version, pluralized, value,
  zero_value, one_value, two_value, few_value, many_value, other_value, deleted
FROM lang_pack_strings
WHERE lang_pack = $1 AND lang_code = $2 AND NOT deleted
ORDER BY key;

-- name: GetLangPackStringsByKeys :many
SELECT
  lang_pack, lang_code, key, version, pluralized, value,
  zero_value, one_value, two_value, few_value, many_value, other_value, deleted
FROM lang_pack_strings
WHERE lang_pack = $1 AND lang_code = $2 AND key = ANY(sqlc.arg(keys)::text[]) AND NOT deleted
ORDER BY key;

-- name: ListLangPackLanguages :many
SELECT
  p.lang_pack,
  p.lang_code,
  p.version,
  p.strings_count,
  COALESCE(
    MAX(s.value) FILTER (WHERE s.key = 'LanguageNameInEnglish'),
    MAX(s.value) FILTER (WHERE s.key = 'Localization.EnglishLanguageName'),
    ''
  )::text AS name,
  CASE
    WHEN p.lang_pack = 'android' THEN COALESCE(
      MAX(s.value) FILTER (WHERE s.key = 'TranslateLanguage' || upper(split_part(replace(p.lang_code, '-', '_'), '_', 1))),
      MAX(s.value) FILTER (WHERE s.key = 'PassportLanguage_' || upper(split_part(replace(p.lang_code, '-', '_'), '_', 1))),
      MAX(s.value) FILTER (WHERE s.key = 'LanguageName'),
      ''
    )
    ELSE COALESCE(
      MAX(s.value) FILTER (WHERE s.key = 'lng_language_name'),
      MAX(s.value) FILTER (WHERE s.key = 'LanguageName'),
      MAX(s.value) FILTER (WHERE s.key = 'Localization.LanguageName'),
      MAX(s.value) FILTER (WHERE s.key = 'TranslateLanguage' || upper(split_part(replace(p.lang_code, '-', '_'), '_', 1))),
      MAX(s.value) FILTER (WHERE s.key = 'PassportLanguage_' || upper(split_part(replace(p.lang_code, '-', '_'), '_', 1))),
      ''
    )
  END::text AS native_name
FROM lang_packs p
LEFT JOIN lang_pack_strings s
  ON s.lang_pack = p.lang_pack
 AND s.lang_code = p.lang_code
 AND s.key = ANY(ARRAY[
   'lng_language_name',
   'LanguageName',
   'LanguageNameInEnglish',
   'Localization.LanguageName',
   'Localization.EnglishLanguageName',
   'TranslateLanguage' || upper(split_part(replace(p.lang_code, '-', '_'), '_', 1)),
   'PassportLanguage_' || upper(split_part(replace(p.lang_code, '-', '_'), '_', 1))
 ]::text[])
 AND NOT s.deleted
WHERE p.lang_pack = $1
GROUP BY p.lang_pack, p.lang_code, p.version, p.strings_count
ORDER BY p.lang_code;
