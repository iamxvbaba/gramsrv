-- Verify that a unique gift resolves to an animated collectible model.
-- Run with: psql "$TELESRV_POSTGRES_DSN" -f scripts/db/check_animation.sql
SELECT u.slug,
       u.gift_id,
       m.id AS model_attr_id,
       m.name AS model_name,
       (m.animation_json IS NOT NULL) AS has_animation,
       length(m.animation_json::text) AS animation_len
FROM public.unique_star_gifts AS u
LEFT JOIN public.star_gift_catalog AS c ON c.gift_id = u.gift_id
LEFT JOIN public.star_gift_collectible_models AS m
  ON m.collectible_revision_id = c.collectible_revision_id
 AND m.name = 'Turtles'
WHERE u.slug = 'heart-locket-4';
