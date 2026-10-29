DROP INDEX wallet_gram_authorization_once;
ALTER TABLE wallet_gram_commands
 DROP COLUMN authorization_hash,
 DROP COLUMN request_hash,
 DROP COLUMN message_random_id,
 DROP COLUMN transfer_date;
