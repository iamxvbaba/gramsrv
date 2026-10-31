-- Story replies (comments): when a user replies to a story, the client sends
-- messages.sendMessage(reply_to=inputReplyToStory{peer, story_id}); the message
-- lands in the private chat with the story's author and is projected as
-- messageReplyStoryHeader. Previously telesrv rejected it outright
-- (STORY_ID_INVALID), so comment sending failed. This reuses the existing reply_to_*
-- column family and adds reply_to_story_id to persist the replied-to story id
-- (always 0 for ordinary message replies). Both boxes (the private-chat body +
-- the per-owner projection) need this column so the story reply header can be
-- restored when re-reading history.
ALTER TABLE public.private_messages ADD COLUMN reply_to_story_id integer DEFAULT 0 NOT NULL;
ALTER TABLE public.message_boxes ADD COLUMN reply_to_story_id integer DEFAULT 0 NOT NULL;
