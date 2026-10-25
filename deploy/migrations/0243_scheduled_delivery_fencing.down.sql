DROP INDEX scheduled_messages_online_idx;
ALTER TABLE scheduled_messages DROP COLUMN generation;
ALTER TABLE scheduled_messages DROP COLUMN manual_dispatch, DROP COLUMN reply_to_story_id, DROP COLUMN reply_forum_topic,
 DROP COLUMN reply_external, DROP COLUMN fwd_channel_post, DROP COLUMN fwd_saved_from_peer_type,
 DROP COLUMN fwd_saved_from_peer_id, DROP COLUMN fwd_saved_from_msg_id;
