ALTER TABLE dispatch_outbox
    DROP CONSTRAINT IF EXISTS dispatch_outbox_exclusion_pair_check;
