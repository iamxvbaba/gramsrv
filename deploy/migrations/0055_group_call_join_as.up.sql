-- join_as: a participant can join as the channel/group itself (anonymous admin
-- semantics). 0 = join as one's own user; the unique key is still
-- (call_id, user_id), and rejoining with a different identity replaces it.
ALTER TABLE group_call_participants
    ADD COLUMN IF NOT EXISTS join_as_channel_id bigint DEFAULT 0 NOT NULL;
