-- Inspect the message_entities columns added for gift captions and entities.
SELECT table_name, column_name, data_type, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema = 'public'
  AND column_name = 'message_entities'
  AND table_name IN ('peer_star_gifts', 'star_gift_purchase_forms')
ORDER BY table_name;

SELECT conname
FROM pg_constraint
WHERE conname IN (
    'peer_star_gifts_message_entities_array_check',
    'star_gift_purchase_forms_message_entities_array_check'
)
ORDER BY conname;
