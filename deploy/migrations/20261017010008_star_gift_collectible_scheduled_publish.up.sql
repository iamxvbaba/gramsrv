-- Deferred/scheduled collectible drop: an admin can publish a collectible pool
-- (models/patterns/backdrops + supply/upgrade price) as a draft now and have it
-- go live automatically at a chosen future time, instead of the pool only ever
-- being able to open the instant the admin clicks publish. The regular
-- (non-collectible) gift itself is unaffected either way -- see
-- star_gift_catalog.collectible_revision_id, which a draft row still leaves
-- untouched until it actually activates.
ALTER TABLE public.star_gift_collectible_revisions
    ADD COLUMN scheduled_publish_at timestamp with time zone;

-- Looked up by the collectible-drop dispatcher every tick; partial index keeps
-- it cheap since almost every revision is 'published' almost all the time.
CREATE INDEX star_gift_collectible_revisions_due_idx
    ON public.star_gift_collectible_revisions(scheduled_publish_at)
    WHERE status = 'draft' AND scheduled_publish_at IS NOT NULL;

-- Extend the existing publish-immutability guard (as last modified by
-- 0093_official_star_gift_attributes) to also freeze scheduled_publish_at
-- once a revision goes live, same as every other definition column. Built on
-- that migration's version deliberately, not 0090's original: 0092/0093 added
-- the "issued must advance by exactly 1" check and the official-provenance
-- columns, and this must not regress either.
CREATE OR REPLACE FUNCTION public.telesrv_guard_collectible_revision() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status = 'published' THEN
            RAISE EXCEPTION 'published collectible revision is immutable';
        END IF;
        RETURN OLD;
    END IF;

    IF OLD.status = 'published' THEN
        IF NEW.gift_id <> OLD.gift_id OR NEW.revision <> OLD.revision OR
           NEW.upgrade_stars <> OLD.upgrade_stars OR NEW.supply_total <> OLD.supply_total OR
           NEW.slug_prefix <> OLD.slug_prefix OR NEW.status <> OLD.status OR
           NEW.created_by <> OLD.created_by OR NEW.command_id <> OLD.command_id OR
           NEW.created_at <> OLD.created_at OR NEW.published_at <> OLD.published_at OR
           NEW.official_gift_id IS DISTINCT FROM OLD.official_gift_id OR
           NEW.source_manifest_sha256 IS DISTINCT FROM OLD.source_manifest_sha256 OR
           NEW.scheduled_publish_at IS DISTINCT FROM OLD.scheduled_publish_at THEN
            RAISE EXCEPTION 'published collectible revision is immutable';
        END IF;
        IF NEW.issued <> OLD.issued + 1 THEN
            RAISE EXCEPTION 'published collectible issuance must advance exactly once';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
