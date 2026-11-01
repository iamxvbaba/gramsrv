-- messages.getRecentLocations reuses the shared media seek index, avoiding a JSONB scan over peer history.
-- category=10 is the server-internal geo_live category, not exposed to ordinary messages.search filters.
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
