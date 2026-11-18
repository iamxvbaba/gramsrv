-- Refuse rollback while free published pools exist; never rewrite their price.
ALTER TABLE star_gift_collectible_revisions
    DROP CONSTRAINT star_gift_collectible_price_check,
    ADD CONSTRAINT star_gift_collectible_price_check CHECK (upgrade_stars > 0);

ALTER TABLE star_gift_purchase_commands DROP COLUMN include_upgrade;

ALTER TABLE star_gift_upgrade_commands
    DROP CONSTRAINT star_gift_upgrade_command_replay_shape_check,
    ADD CONSTRAINT star_gift_upgrade_command_replay_shape_check CHECK (
        (require_prepaid AND form_id = 0 AND charge_stars = 0)
        OR (NOT require_prepaid AND form_id <> 0 AND charge_stars > 0)
    );
