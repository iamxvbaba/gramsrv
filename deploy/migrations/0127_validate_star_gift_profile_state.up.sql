-- 0126 repaired historical rows and installed the constraint as NOT VALID so
-- it could coexist with the deferrable unique-gift owner trigger in one
-- migration transaction. Validate after that transaction has committed.
ALTER TABLE peer_star_gifts
    VALIDATE CONSTRAINT peer_star_gifts_hidden_unpinned_check;
