-- Composite account rating, projected into userFull.stars_rating (StarsRating:
-- level, stars, current_level_stars, next_level_stars) and
-- userFull.stars_my_pending_rating.
--
-- account_rating is a derived read model: it can always be rebuilt from the
-- contributing sources (stars_transactions, message counts, moderation state)
-- plus the manual adjustments recorded in account_rating_events. Every stored
-- component is kept separately so the admin panel can show why a level was
-- reached, and so recomputing one signal never silently discards another.
--
-- 'stars' is the composite score the protocol exposes, not a wallet balance:
-- clients render it as the rating number and derive progress from
-- current_level_stars/next_level_stars.

CREATE TABLE public.account_rating (
    user_id bigint PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    level integer NOT NULL DEFAULT 0 CHECK (level >= 0),
    stars bigint NOT NULL DEFAULT 0,
    current_level_stars bigint NOT NULL DEFAULT 0 CHECK (current_level_stars >= 0),
    -- NULL means the top level has been reached: the flag is omitted in TL.
    next_level_stars bigint CHECK (next_level_stars IS NULL OR next_level_stars > 0),
    CHECK (next_level_stars IS NULL OR next_level_stars > current_level_stars),
    -- Signed contributions. penalty_component is stored as a non-negative
    -- magnitude and subtracted, so an audit never has to guess the sign.
    stars_component bigint NOT NULL DEFAULT 0 CHECK (stars_component >= 0),
    activity_component bigint NOT NULL DEFAULT 0 CHECK (activity_component >= 0),
    penalty_component bigint NOT NULL DEFAULT 0 CHECK (penalty_component >= 0),
    manual_component bigint NOT NULL DEFAULT 0,
    -- Rating earned but not yet applied to the visible level.
    pending_stars bigint NOT NULL DEFAULT 0,
    pending_date timestamptz,
    CHECK ((pending_stars = 0 AND pending_date IS NULL) OR (pending_stars <> 0 AND pending_date IS NOT NULL)),
    computed_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE INDEX account_rating_leaderboard_idx
    ON public.account_rating (level DESC, stars DESC, user_id);

CREATE INDEX account_rating_stale_idx
    ON public.account_rating (computed_at, user_id);

-- Append-only contribution log. 'manual' rows are admin adjustments and are the
-- only rows that survive a full recompute; command_key gives them the same
-- replay safety as other admin commands.
CREATE TABLE public.account_rating_events (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('stars', 'activity', 'moderation', 'manual', 'recompute')),
    amount bigint NOT NULL,
    reason text NOT NULL DEFAULT '' CHECK (octet_length(reason) <= 512),
    actor text NOT NULL DEFAULT '' CHECK (octet_length(actor) <= 128),
    command_key text CHECK (command_key IS NULL OR octet_length(command_key) BETWEEN 1 AND 128),
    created_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX account_rating_events_command_idx
    ON public.account_rating_events (command_key)
    WHERE command_key IS NOT NULL;

CREATE INDEX account_rating_events_user_idx
    ON public.account_rating_events (user_id, id DESC);

CREATE INDEX account_rating_events_kind_idx
    ON public.account_rating_events (kind, created_at DESC, id DESC);

-- Rating recompute counts upheld cases per target. The existing target index is
-- partial on the undecided states, so without this one the count degrades to a
-- sequential scan on every recompute.
CREATE INDEX moderation_cases_target_history_idx
    ON public.moderation_cases (target_peer_type, target_peer_id)
    WHERE status IN ('action_pending', 'action_failed', 'resolved');
