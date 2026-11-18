-- Attribute pools for auction gifts may have no upgrade fee.
ALTER TABLE star_gift_collectible_revisions
    DROP CONSTRAINT star_gift_collectible_price_check,
    ADD CONSTRAINT star_gift_collectible_price_check CHECK (upgrade_stars >= 0);

-- Preserve the requested option even when its price is zero.
ALTER TABLE star_gift_purchase_commands ADD COLUMN include_upgrade boolean NOT NULL DEFAULT false;
UPDATE star_gift_purchase_commands c
SET include_upgrade = COALESCE(
    (SELECT f.include_upgrade FROM star_gift_purchase_forms f
     WHERE f.buyer_user_id = c.buyer_user_id AND f.form_id = c.form_id),
    c.charge_stars > r.stars
)
FROM peer_star_gifts s
JOIN star_gift_catalog_revisions r ON r.id = s.catalog_revision_id
WHERE s.id = c.saved_gift_id;

ALTER TABLE star_gift_upgrade_commands
    DROP CONSTRAINT star_gift_upgrade_command_replay_shape_check,
    ADD CONSTRAINT star_gift_upgrade_command_replay_shape_check CHECK (
        (require_prepaid AND form_id = 0 AND charge_stars = 0)
        OR (NOT require_prepaid AND charge_stars = 0)
        OR (NOT require_prepaid AND form_id <> 0 AND charge_stars > 0)
    );
