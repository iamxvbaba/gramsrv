DROP INDEX scheduled_messages_owner_random_idx;
CREATE UNIQUE INDEX scheduled_messages_owner_random_pending_idx ON scheduled_messages(owner_user_id,random_id) WHERE random_id<>0 AND state IN ('pending','dispatching');
ALTER TABLE scheduled_messages DROP COLUMN request_fingerprint;
