-- DrKLO's profile-header path creates collectible patterns with
-- CACHE_TYPE_ALERT_PREVIEW_STATIC.  For application/x-tgsticker that client
-- only loads the main TGS when Document.thumbs is non-empty; an empty list is
-- routed to a null thumbnail location and the pattern remains a flat backdrop.
--
-- Document metadata is cached by id, so adding a thumb to an already published
-- id would not repair existing Android installations.  Clone every affected
-- published pattern under a new immutable identity, add an inline PhotoPathSize
-- placeholder, alias the unchanged main blob, and converge active/durable emoji
-- status snapshots to the new id.
CREATE TABLE public.star_gift_pattern_preview_document_repairs (
    old_document_id bigint PRIMARY KEY,
    new_document_id bigint UNIQUE NOT NULL,
    repaired_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT star_gift_pattern_preview_document_repairs_ids_check CHECK (
        old_document_id > 0 AND new_document_id > 0 AND old_document_id <> new_document_id
    )
);

INSERT INTO public.star_gift_pattern_preview_document_repairs (old_document_id, new_document_id)
SELECT DISTINCT
    p.document_id,
    ((('x' || substr(md5(p.document_id::text || ':collectible-pattern-android-preview:v2'), 1, 16))
        ::bit(64)::bigint) & 9223372036854775807)
FROM public.star_gift_collectible_patterns p
JOIN public.documents d ON d.id = p.document_id
WHERE jsonb_array_length(d.thumbs) = 0;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.star_gift_pattern_preview_document_repairs r
        JOIN public.documents d ON d.id = r.new_document_id
    ) OR EXISTS (
        SELECT 1
        FROM public.star_gift_pattern_preview_document_repairs r
        JOIN public.file_blobs f
          ON f.location_key = 'doc:' || r.new_document_id::text
          OR f.location_key LIKE 'doc:' || r.new_document_id::text || ':%'
    ) THEN
        RAISE EXCEPTION 'collectible pattern preview document repair id collision';
    END IF;
END
$$;

INSERT INTO public.documents (
    id, access_hash, file_reference, date, mime_type, size, dc_id,
    attributes, thumbs, created_at
)
SELECT
    r.new_document_id,
    d.access_hash,
    d.file_reference,
    d.date,
    d.mime_type,
    d.size,
    d.dc_id,
    d.attributes,
    jsonb_build_array(jsonb_build_object(
        'kind', 'path',
        'type', 'j',
        'bytes', 'GQalBdxhTX54SARIBGNsfE4Imk4HooCjlLqhhYOHSIxMjEybVa1VkICfhqqRqquGigRYjgFNkXmHA0cGhwM='
    )),
    now()
FROM public.star_gift_pattern_preview_document_repairs r
JOIN public.documents d ON d.id = r.old_document_id;

INSERT INTO public.file_blobs (
    location_key, backend, object_key, size, sha256, mime_type, created_at
)
SELECT
    'doc:' || r.new_document_id::text ||
        substr(f.location_key, length('doc:' || r.old_document_id::text) + 1),
    f.backend,
    f.object_key,
    f.size,
    f.sha256,
    f.mime_type,
    now()
FROM public.star_gift_pattern_preview_document_repairs r
JOIN public.file_blobs f
  ON f.location_key = 'doc:' || r.old_document_id::text
  OR f.location_key LIKE 'doc:' || r.old_document_id::text || ':%';

ALTER TABLE public.star_gift_collectible_patterns
    DISABLE TRIGGER star_gift_collectible_pattern_guard;
UPDATE public.star_gift_collectible_patterns p
SET document_id = r.new_document_id
FROM public.star_gift_pattern_preview_document_repairs r
WHERE p.document_id = r.old_document_id;
ALTER TABLE public.star_gift_collectible_patterns
    ENABLE TRIGGER star_gift_collectible_pattern_guard;

CREATE TEMP TABLE telesrv_pattern_preview_repaired_wearers ON COMMIT DROP AS
SELECT u.id AS user_id, r.old_document_id, r.new_document_id
FROM public.users u
JOIN public.star_gift_pattern_preview_document_repairs r
  ON (u.emoji_status_collectible->>'pattern_document_id')::bigint = r.old_document_id
WHERE u.emoji_status_collectible_id IS NOT NULL;

UPDATE public.users u
SET emoji_status_collectible = jsonb_set(
        u.emoji_status_collectible,
        '{pattern_document_id}',
        to_jsonb(w.new_document_id),
        false
    ),
    updated_at = now()
FROM telesrv_pattern_preview_repaired_wearers w
WHERE u.id = w.user_id;

UPDATE public.user_update_events e
SET emoji_status_payload = jsonb_set(
        e.emoji_status_payload,
        '{collectible,pattern_document_id}',
        to_jsonb(r.new_document_id),
        false
    )
FROM public.star_gift_pattern_preview_document_repairs r
WHERE e.event_type = 'user_emoji_status'
  AND (e.emoji_status_payload #>> '{collectible,pattern_document_id}')::bigint = r.old_document_id;

INSERT INTO public.user_update_watermarks (user_id, contiguous_pts)
SELECT user_id, 0 FROM telesrv_pattern_preview_repaired_wearers
ON CONFLICT (user_id) DO NOTHING;

CREATE TEMP TABLE telesrv_pattern_preview_correction_events (
    user_id bigint PRIMARY KEY,
    pts integer NOT NULL
) ON COMMIT DROP;

WITH bumped AS (
    UPDATE public.user_update_watermarks w
       SET contiguous_pts = contiguous_pts + 1,
           updated_at = now()
      FROM telesrv_pattern_preview_repaired_wearers wearer
     WHERE w.user_id = wearer.user_id
     RETURNING w.user_id, w.contiguous_pts
)
INSERT INTO telesrv_pattern_preview_correction_events (user_id, pts)
SELECT user_id, contiguous_pts FROM bumped;

INSERT INTO public.user_update_events (
    user_id, pts, pts_count, date, event_type,
    peer_type, peer_id, emoji_status_payload
)
SELECT
    c.user_id,
    c.pts,
    1,
    EXTRACT(EPOCH FROM clock_timestamp())::integer,
    'user_emoji_status',
    'user',
    c.user_id,
    jsonb_strip_nulls(jsonb_build_object(
        'document_id', u.emoji_status_document_id,
        'until', CASE WHEN u.emoji_status_until > 0 THEN u.emoji_status_until ELSE NULL END,
        'collectible', u.emoji_status_collectible
    ))
FROM telesrv_pattern_preview_correction_events c
JOIN public.users u ON u.id = c.user_id;

INSERT INTO public.dispatch_outbox (
    target_user_id, pts, event_type, exclude_auth_key_id, exclude_session_id
)
SELECT user_id, pts, 'user_emoji_status', 0, 0
FROM telesrv_pattern_preview_correction_events;
