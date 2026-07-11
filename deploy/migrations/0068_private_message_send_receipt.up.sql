-- Exact random_id replay must not rebuild the original send result from mutable message_boxes:
-- edit advances box pts/body and delete hides the row. Keep the immutable allocation receipt on
-- the shared private message instead; 0073 adds the immutable first snapshot/delete receipt used
-- to build the client acknowledgement without allocating new pts or update facts.
ALTER TABLE public.private_messages
    ADD COLUMN sender_box_id integer NOT NULL DEFAULT 0 CHECK (sender_box_id >= 0),
    ADD COLUMN sender_pts integer NOT NULL DEFAULT 0 CHECK (sender_pts >= 0),
    ADD COLUMN recipient_box_id integer NOT NULL DEFAULT 0 CHECK (recipient_box_id >= 0),
    ADD COLUMN recipient_pts integer NOT NULL DEFAULT 0 CHECK (recipient_pts >= 0);

-- Keep zero defaults for writers released before this migration.  Zero is not
-- a valid immutable receipt: when a row has a valid request fingerprint but a
-- legacy writer omitted these columns, the new replay path fails fast instead
-- of deriving the first response from mutable message_boxes.  New writers
-- always persist positive sender receipt values before commit.
