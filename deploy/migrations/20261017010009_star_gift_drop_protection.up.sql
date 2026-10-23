-- Anti-scalping knobs for a limited-edition drop: an admin-editable per-user
-- lifetime cap (limited_per_user/per_user_total already existed on the
-- catalog revision but had no admin command to set them after import) plus a
-- NEW sliding-window rate limit (rate_limit_count purchases of this gift per
-- rate_limit_window_seconds, per buyer). Both default to disabled (0/0), so
-- an un-edited deployment behaves exactly as before.
ALTER TABLE public.star_gift_catalog_revisions
    ADD COLUMN rate_limit_count integer DEFAULT 0 NOT NULL,
    ADD COLUMN rate_limit_window_seconds integer DEFAULT 0 NOT NULL,
    ADD CONSTRAINT star_gift_catalog_revision_rate_limit_check CHECK (
        rate_limit_count >= 0 AND rate_limit_window_seconds >= 0 AND
        ((rate_limit_count > 0 AND rate_limit_window_seconds > 0) OR
         (rate_limit_count = 0 AND rate_limit_window_seconds = 0))
    );

-- window_started_at/window_count track the current buyer's sliding window in
-- the same per-(user,gift) row the lifetime purchased_count already lives
-- in, so the purchase transaction can check and advance both counters with
-- one atomic UPSERT (see internal/store/postgres/star_gift_purchase.go). Business-clock integers
-- (unix seconds), matching every other *_date column on this aggregate --
-- not timestamptz, which the rest of this table never uses for buyer-facing
-- time.
ALTER TABLE public.star_gift_user_purchases
    ADD COLUMN window_started_at integer DEFAULT 0 NOT NULL,
    ADD COLUMN window_count integer DEFAULT 0 NOT NULL,
    ADD CONSTRAINT star_gift_user_purchases_window_check CHECK (
        window_started_at >= 0 AND window_count >= 0
    );
