-- v2 编号 0264；移植自 main 0240_channel_message_fact_check（main 6446194d，B11b），见 docs/main-v2-sync.md。
-- Fact checks: the protocol has carried factCheck and messages.editFactCheck /
-- messages.deleteFactCheck / messages.getFactCheck since Layer 225; the server only
-- now implements them. The label is attached to the message itself, so
-- every subscriber of the channel reads the same verdict. The empty object is
-- the "no fact check" state; a non-empty object carries need_check/country/
-- text/entities/hash.
ALTER TABLE channel_messages ADD COLUMN factcheck jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE channel_messages ADD CONSTRAINT channel_messages_factcheck_check CHECK (jsonb_typeof(factcheck) = 'object');
