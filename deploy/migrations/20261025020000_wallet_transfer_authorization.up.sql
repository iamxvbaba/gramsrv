-- Existing receipts remain historical. New transfers require a single-use
-- SRP temporary-password grant and bind every retry to the exact request.
ALTER TABLE wallet_gram_commands
 ADD COLUMN authorization_hash bytea,
 ADD COLUMN request_hash bytea,
 ADD COLUMN message_random_id bigint,
 ADD COLUMN transfer_date integer,
 ADD CONSTRAINT wallet_gram_authorization_hash_length CHECK (authorization_hash IS NULL OR octet_length(authorization_hash)=32),
 ADD CONSTRAINT wallet_gram_request_hash_length CHECK (request_hash IS NULL OR octet_length(request_hash)=32);
CREATE UNIQUE INDEX wallet_gram_authorization_once ON wallet_gram_commands(from_user_id, authorization_hash) WHERE authorization_hash IS NOT NULL;
