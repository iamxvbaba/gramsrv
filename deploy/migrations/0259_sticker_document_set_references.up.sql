-- v2 编号 0259；移植自 main 0235_sticker_document_set_references（main 9a6059db，B8），见 docs/main-v2-sync.md。
-- Different source-account exports overwrote the same document with conflicting
-- set credentials. Repair proven catalog references, preserving attribute order.
WITH repaired AS (
    SELECT d.id, jsonb_agg(
        CASE WHEN s.id IS NOT NULL THEN
            jsonb_set(a.value, '{sticker_set_access_hash}', to_jsonb(s.access_hash))
        ELSE a.value END ORDER BY a.ordinality
    ) AS attributes
    FROM documents d
    CROSS JOIN LATERAL jsonb_array_elements(d.attributes) WITH ORDINALITY AS a(value, ordinality)
    LEFT JOIN sticker_sets s
        ON a.value->>'kind' IN ('sticker', 'custom_emoji')
        AND s.id = (a.value->>'sticker_set_id')::bigint
    GROUP BY d.id
    HAVING bool_or(s.id IS NOT NULL AND
        (a.value->>'sticker_set_access_hash')::bigint IS DISTINCT FROM s.access_hash)
)
UPDATE documents d SET attributes = r.attributes FROM repaired r WHERE d.id = r.id;
