-- v2 编号 0261；移植自 main 0237_rating_level_scale（main c34bc584，B9），见 docs/main-v2-sync.md。
-- Retune the local composite rating from a 50-level ladder to a 100-level one.
--
-- The score -> level mapping is a server-side policy curve, but account_rating
-- stores the derived level, so every existing row still carries a level produced
-- by the old curve. Until the background worker happened to reach a row (bounded
-- by TELESRV_RATING_STALE_AFTER, so hours), the profile projection and the admin
-- leaderboard would keep showing the old, coarser levels -- and the leaderboard
-- ordering is wrong in between, because it is keyed on level.
--
-- account_rating is a derived read model: level, current_level_stars and
-- next_level_stars are pure functions of stars, so they can be re-derived here
-- without touching the score, its components or the adjustment ledger. This
-- mirrors domain.AccountRatingLevelForStars exactly: level = min(100, floor(
-- sqrt(max(stars, 0) / 25))), current = 25 * level^2 and next = 25 * (level+1)^2
-- while level < 100, NULL above it.
--
-- The thresholds involved are small exact integers, so sqrt() on the double
-- representation is exact and a score sitting precisely on a threshold keeps the
-- level it just reached instead of dropping back one rung.
WITH remapped AS (
    SELECT
        user_id,
        LEAST(100, GREATEST(0, FLOOR(SQRT(GREATEST(0, stars)::double precision / 25))))::integer AS level
    FROM public.account_rating
)
UPDATE public.account_rating AS r
SET level = m.level,
    current_level_stars = 25 * m.level * m.level,
    next_level_stars = CASE WHEN m.level >= 100 THEN NULL ELSE 25 * (m.level + 1) * (m.level + 1) END
FROM remapped AS m
WHERE r.user_id = m.user_id;
