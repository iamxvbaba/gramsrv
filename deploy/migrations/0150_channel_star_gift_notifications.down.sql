DELETE FROM star_gift_user_message_refs refs
USING peer_star_gifts gift
WHERE gift.id = refs.saved_gift_id
  AND gift.owner_peer_type = 'channel';

COMMENT ON TABLE star_gift_user_message_refs IS
    'Owner-local private service-message aliases to user-owned saved gift aggregates.';

DROP TABLE IF EXISTS star_gift_channel_notification_jobs;
