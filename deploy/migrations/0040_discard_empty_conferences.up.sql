WITH stale AS (
    SELECT c.call_id
    FROM public.group_calls c
    WHERE c.kind = 'conference'::text
      AND c.state = 'active'::text
      AND EXISTS (
          SELECT 1
          FROM public.group_call_participants p
          WHERE p.call_id = c.call_id
      )
      AND NOT EXISTS (
          SELECT 1
          FROM public.group_call_participants p
          WHERE p.call_id = c.call_id
            AND NOT p.left_call
      )
)
UPDATE public.group_calls c
SET state = 'discarded'::text,
    discarded_at = CASE
        WHEN c.discarded_at = 0 THEN EXTRACT(EPOCH FROM now())::integer
        ELSE c.discarded_at
    END,
    duration = CASE
        WHEN c.duration = 0 THEN GREATEST(0, EXTRACT(EPOCH FROM now())::integer - c.created_at)
        ELSE c.duration
    END,
    participants_count = 0,
    version = c.version + 1
FROM stale
WHERE c.call_id = stale.call_id;
