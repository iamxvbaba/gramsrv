-- Persist the immutable client intent beside a channel random_id receipt.
--
-- The empty default is intentional for rolling deploys: binaries that predate
-- this migration can continue to insert channel/service messages.  Empty
-- fingerprints are legacy/unknown receipts and the replay path rejects them;
-- it never guesses intent from an editable message projection.
ALTER TABLE public.channel_messages
    ADD COLUMN request_fingerprint bytea NOT NULL DEFAULT '\x';

ALTER TABLE public.channel_messages
    ADD CONSTRAINT channel_messages_request_fingerprint_size
    CHECK (octet_length(request_fingerprint) IN (0, 32)) NOT VALID;

-- NOT VALID avoids a blocking historical-table validation scan during the
-- rolling migration while PostgreSQL still enforces the check for every new
-- or updated row. A later maintenance window may validate it online.
