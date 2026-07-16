ALTER TABLE public.peer_star_gifts
    ADD COLUMN IF NOT EXISTS prepaid_upgrade_stars bigint DEFAULT 0 NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.peer_star_gifts'::regclass
          AND conname = 'peer_star_gifts_prepaid_upgrade_check'
    ) THEN
        ALTER TABLE public.peer_star_gifts
            ADD CONSTRAINT peer_star_gifts_prepaid_upgrade_check CHECK (prepaid_upgrade_stars >= 0);
    END IF;
END;
$$;
