ALTER TABLE channels
    ADD COLUMN IF NOT EXISTS private_chat_forbidden boolean NOT NULL DEFAULT false;
