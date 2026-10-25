-- v2 编号 0243；移植自 main 0214_scheduled_delivery_fencing（main e5971805），见 docs/main-v2-sync.md。
ALTER TABLE scheduled_messages ADD COLUMN generation bigint NOT NULL DEFAULT 0;
CREATE INDEX scheduled_messages_online_idx ON scheduled_messages (peer_id, owner_user_id, scheduled_id)
WHERE schedule_date = 2147483646 AND state IN ('pending', 'dispatching');
ALTER TABLE scheduled_messages
 ADD COLUMN manual_dispatch boolean NOT NULL DEFAULT false,
 ADD COLUMN reply_to_story_id integer NOT NULL DEFAULT 0,
 ADD COLUMN reply_forum_topic boolean NOT NULL DEFAULT false,
 ADD COLUMN reply_external jsonb NOT NULL DEFAULT '{}',
 ADD COLUMN fwd_channel_post integer NOT NULL DEFAULT 0,
 ADD COLUMN fwd_saved_from_peer_type varchar(16) NOT NULL DEFAULT '',
 ADD COLUMN fwd_saved_from_peer_id bigint NOT NULL DEFAULT 0,
 ADD COLUMN fwd_saved_from_msg_id integer NOT NULL DEFAULT 0;
