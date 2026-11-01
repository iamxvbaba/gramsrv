-- Channel direct messages: a parent broadcast channel is linked to a monoforum
-- virtual channel that carries subscriber direct messages. monoforum=true marks
-- the channel as a monoforum; linked_monoforum_id bidirectionally links the parent
-- channel <-> monoforum.
ALTER TABLE public.channels
    ADD COLUMN monoforum boolean DEFAULT false NOT NULL;
ALTER TABLE public.channels
    ADD COLUMN linked_monoforum_id bigint DEFAULT 0 NOT NULL;
