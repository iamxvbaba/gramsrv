-- Durable edit events and repaired user gift projections are intentionally not
-- rewound. Dropping the new lookup/index constraints is sufficient rollback.
ALTER TABLE peer_star_gifts
    DROP CONSTRAINT IF EXISTS peer_star_gifts_hidden_unpinned_check;

DROP TABLE IF EXISTS star_gift_user_message_refs;
