-- Link preview (webpage preview) resolution cache. The key is a 63-bit hash of the
-- normalized URL; the same URL dedups to one row across sends and instances,
-- avoiding repeated external fetches for the same link. snapshot is the complete
-- domain.MessageWebPage (parsed card fields plus embedded preview-image
-- references), isomorphic to the message media column, so reads can project it to
-- messageMediaWebPage without re-fetching or rehydrating.
--
-- web_page_id is derived from url_hash (the two share a value), so the pending
-- placeholder and the done resolution carry the same webPage id, and clients
-- associate the placeholder with its resolution by id (see the async-backfill stage).
-- state is redundant so later sweeps/refreshes can filter by pending/empty. snapshot
-- is immutable (content-addressed preview-image blobs are persisted separately in
-- photos/blob storage); cross-instance dedup relies on the url_hash unique
-- constraint plus a per-instance TTL, so this table needs no read-model NOTIFY trigger.
CREATE TABLE public.web_pages (
    url_hash     bigint PRIMARY KEY,
    web_page_id  bigint NOT NULL,
    state        text   NOT NULL,
    snapshot     jsonb  NOT NULL DEFAULT '{}'::jsonb,
    created_at   bigint NOT NULL DEFAULT 0,
    refreshed_at bigint NOT NULL DEFAULT 0
);
