CREATE TEMP TABLE telesrv_pattern_document_repair_rollback ON COMMIT DROP AS
SELECT old_document_id, new_document_id
FROM public.star_gift_pattern_document_repairs;

CREATE TEMP TABLE telesrv_rollback_collectible_wearers ON COMMIT DROP AS
SELECT u.id AS user_id, r.old_document_id, r.new_document_id
FROM public.users u
JOIN telesrv_pattern_document_repair_rollback r
  ON (u.emoji_status_collectible->>'pattern_document_id')::bigint = r.new_document_id
WHERE u.emoji_status_collectible_id IS NOT NULL;

UPDATE public.users u
SET emoji_status_collectible = jsonb_set(
        u.emoji_status_collectible,
        '{pattern_document_id}',
        to_jsonb(w.old_document_id),
        false
    ),
    updated_at = now()
FROM telesrv_rollback_collectible_wearers w
WHERE u.id = w.user_id;

UPDATE public.user_update_events e
SET emoji_status_payload = jsonb_set(
        e.emoji_status_payload,
        '{collectible,pattern_document_id}',
        to_jsonb(r.old_document_id),
        false
    )
FROM telesrv_pattern_document_repair_rollback r
WHERE e.event_type = 'user_emoji_status'
  AND (e.emoji_status_payload #>> '{collectible,pattern_document_id}')::bigint = r.new_document_id;

ALTER TABLE public.star_gift_collectible_patterns
    DISABLE TRIGGER star_gift_collectible_pattern_guard;
UPDATE public.star_gift_collectible_patterns p
SET document_id = r.old_document_id
FROM telesrv_pattern_document_repair_rollback r
WHERE p.document_id = r.new_document_id;
ALTER TABLE public.star_gift_collectible_patterns
    ENABLE TRIGGER star_gift_collectible_pattern_guard;

INSERT INTO public.user_update_watermarks (user_id, contiguous_pts)
SELECT user_id, 0 FROM telesrv_rollback_collectible_wearers
ON CONFLICT (user_id) DO NOTHING;

CREATE TEMP TABLE telesrv_collectible_pattern_rollback_events (
    user_id bigint PRIMARY KEY,
    pts integer NOT NULL
) ON COMMIT DROP;

WITH bumped AS (
    UPDATE public.user_update_watermarks w
       SET contiguous_pts = contiguous_pts + 1,
           updated_at = now()
      FROM telesrv_rollback_collectible_wearers wearer
     WHERE w.user_id = wearer.user_id
     RETURNING w.user_id, w.contiguous_pts
)
INSERT INTO telesrv_collectible_pattern_rollback_events (user_id, pts)
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
FROM telesrv_collectible_pattern_rollback_events c
JOIN public.users u ON u.id = c.user_id;

INSERT INTO public.dispatch_outbox (
    target_user_id, pts, event_type, exclude_auth_key_id, exclude_session_id
)
SELECT user_id, pts, 'user_emoji_status', 0, 0
FROM telesrv_collectible_pattern_rollback_events;

DELETE FROM public.file_blobs f
USING telesrv_pattern_document_repair_rollback r
WHERE f.location_key = 'doc:' || r.new_document_id::text
   OR f.location_key LIKE 'doc:' || r.new_document_id::text || ':%';

DROP TABLE public.star_gift_pattern_document_repairs;

DELETE FROM public.documents d
USING telesrv_pattern_document_repair_rollback r
WHERE d.id = r.new_document_id;
