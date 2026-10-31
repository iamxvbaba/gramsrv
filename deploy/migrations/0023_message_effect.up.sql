-- Message effect: messages.sendMessage/sendMedia's effect:flags2.2?long, an
-- animation effect exclusive to 1-1 private chats (🎉/👍 etc., played on both the
-- sender's and receiver's side). All official clients only show the effect picker
-- in private chats and never render it for groups/channels, so only the two
-- private-chat tables are mirrored (shared body + per-owner inbox) and no column
-- is added to channel_messages. Mirrors the scalar-column style of
-- via_bot_id / grouped_id (bigint, 0 means no effect).
ALTER TABLE public.private_messages ADD COLUMN effect bigint DEFAULT 0 NOT NULL;
ALTER TABLE public.message_boxes ADD COLUMN effect bigint DEFAULT 0 NOT NULL;
