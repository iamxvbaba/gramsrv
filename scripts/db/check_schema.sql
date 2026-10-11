-- Read-only post-migration smoke checks for the schema shapes used by the
-- collectible gift and message-entity paths. Every row should report true or
-- a non-zero count on a current database.
SELECT 'suggested_post_approvals_shape_check has schedule_date' AS check_name,
       EXISTS (
           SELECT 1
           FROM pg_constraint
           WHERE conname = 'suggested_post_approvals_shape_check'
             AND pg_get_constraintdef(oid) LIKE '%schedule_date%'
       ) AS passed
UNION ALL
SELECT 'bots.commands contains done',
       EXISTS (
           SELECT 1
           FROM public.bots
           WHERE commands::text LIKE '%done%'
       )
UNION ALL
SELECT 'message_entities constraints installed',
       (SELECT count(*) = 2
          FROM pg_constraint
         WHERE conname IN (
             'peer_star_gifts_message_entities_array_check',
             'star_gift_purchase_forms_message_entities_array_check'
         ))
UNION ALL
SELECT 'message_entities columns installed',
       (SELECT count(*) = 2
          FROM information_schema.columns
         WHERE table_schema = 'public'
           AND column_name = 'message_entities'
           AND table_name IN ('peer_star_gifts', 'star_gift_purchase_forms'));

SELECT version, dirty
FROM public.schema_migrations;
