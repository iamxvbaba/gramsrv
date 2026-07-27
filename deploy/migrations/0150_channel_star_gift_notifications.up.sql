-- Channel gift purchases commit a durable recipient snapshot before any
-- private notification is attempted. Delivery is idempotent and restart-safe:
-- the deterministic private-send replay is authoritative if a process stops
-- after writing the message but before marking this job delivered.
CREATE TABLE star_gift_channel_notification_jobs (
    saved_gift_id bigint NOT NULL REFERENCES peer_star_gifts(id) ON DELETE CASCADE,
    target_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    gift_date integer NOT NULL,
    action jsonb NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at integer NOT NULL,
    lease_until integer NOT NULL DEFAULT 0,
    delivered_at integer NOT NULL DEFAULT 0,
    message_id integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (saved_gift_id, target_user_id),
    CONSTRAINT star_gift_channel_notification_jobs_state_check CHECK (
        saved_gift_id > 0
        AND target_user_id > 0
        AND gift_date > 0
        AND attempts >= 0
        AND next_attempt_at > 0
        AND lease_until >= 0
        AND delivered_at >= 0
        AND message_id >= 0
        AND ((delivered_at = 0 AND message_id = 0) OR (delivered_at > 0 AND message_id > 0))
    )
);

CREATE INDEX star_gift_channel_notification_jobs_pending_idx
    ON star_gift_channel_notification_jobs(next_attempt_at, lease_until, saved_gift_id, target_user_id)
    WHERE delivered_at = 0;

COMMENT ON TABLE star_gift_channel_notification_jobs IS
    'Purchase-time snapshot of per-admin channel gift notification intents; delivery uses deterministic private-message replay.';

COMMENT ON TABLE star_gift_user_message_refs IS
    'Viewer-local private service-message aliases to saved gift aggregates; the saved gift owner may be that user or an authorized channel.';

-- Existing deployments/users already have channel prepaid/upgrade notifications.
-- Backfill only aliases that are cryptographically unnecessary to guess: the
-- persisted action itself names a channel and saved_id, and that exact pair
-- must resolve to one saved gift. This changes no message, PTS or outbox row.
WITH ordinary AS (
    SELECT box.owner_user_id,
           box.box_id,
           gift.id AS saved_gift_id
    FROM message_boxes box
    JOIN peer_star_gifts gift
      ON gift.owner_peer_type = 'channel'
     AND (box.media #>> '{service_action,star_gift,peer_channel_id}') ~ '^[1-9][0-9]*$'
     AND gift.owner_peer_id = (box.media #>> '{service_action,star_gift,peer_channel_id}')::bigint
     AND (box.media #>> '{service_action,star_gift,saved_id}') ~ '^[1-9][0-9]*$'
     AND gift.saved_id = (box.media #>> '{service_action,star_gift,saved_id}')::bigint
    WHERE NOT box.deleted
      AND box.media #>> '{service_action,kind}' = 'star_gift'
      AND gift.lifecycle_status = 'active'
), collectible AS (
    SELECT box.owner_user_id,
           box.box_id,
           gift.id AS saved_gift_id
    FROM message_boxes box
    JOIN peer_star_gifts gift
      ON gift.owner_peer_type = 'channel'
     AND box.media #>> '{service_action,star_gift_unique,peer,Type}' = 'channel'
     AND (box.media #>> '{service_action,star_gift_unique,peer,ID}') ~ '^[1-9][0-9]*$'
     AND gift.owner_peer_id = (box.media #>> '{service_action,star_gift_unique,peer,ID}')::bigint
     AND (box.media #>> '{service_action,star_gift_unique,saved_id}') ~ '^[1-9][0-9]*$'
     AND gift.saved_id = (box.media #>> '{service_action,star_gift_unique,saved_id}')::bigint
     AND (box.media #>> '{service_action,star_gift_unique,gift,ID}') ~ '^[1-9][0-9]*$'
     AND gift.unique_gift_id = (box.media #>> '{service_action,star_gift_unique,gift,ID}')::bigint
    WHERE NOT box.deleted
      AND box.media #>> '{service_action,kind}' = 'star_gift_unique'
      AND gift.lifecycle_status = 'active'
), aliases AS (
    SELECT * FROM ordinary
    UNION
    SELECT * FROM collectible
)
INSERT INTO star_gift_user_message_refs(owner_user_id, msg_id, saved_gift_id)
SELECT owner_user_id, box_id, saved_gift_id
FROM aliases
ON CONFLICT(owner_user_id, msg_id) DO UPDATE
SET saved_gift_id = EXCLUDED.saved_gift_id
WHERE star_gift_user_message_refs.saved_gift_id = EXCLUDED.saved_gift_id;
