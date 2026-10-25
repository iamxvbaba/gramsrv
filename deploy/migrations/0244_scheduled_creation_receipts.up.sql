-- v2 编号 0244；移植自 main 0216_scheduled_creation_receipts（main e5971805），见 docs/main-v2-sync.md。
ALTER TABLE scheduled_messages ADD COLUMN request_fingerprint bytea;
DROP INDEX scheduled_messages_owner_random_pending_idx;
CREATE UNIQUE INDEX scheduled_messages_owner_random_idx ON scheduled_messages(owner_user_id,random_id) WHERE random_id<>0;
