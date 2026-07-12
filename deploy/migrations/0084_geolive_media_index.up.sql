-- messages.getRecentLocations 复用共享媒体 seek index，避免在 peer 历史上扫描 JSONB。
-- category=10 是服务端内部 geo_live 类别，不暴露给普通 messages.search filter。
INSERT INTO message_box_media (owner_user_id, box_id, peer_id, category, message_date)
SELECT owner_user_id, box_id, peer_id, 10, message_date
FROM message_boxes
WHERE NOT deleted
  AND media ->> 'kind' = 'geo_live'
ON CONFLICT DO NOTHING;

INSERT INTO channel_message_media (channel_id, id, category, message_date)
SELECT channel_id, id, 10, message_date
FROM channel_messages
WHERE NOT deleted
  AND media ->> 'kind' = 'geo_live'
ON CONFLICT DO NOTHING;
