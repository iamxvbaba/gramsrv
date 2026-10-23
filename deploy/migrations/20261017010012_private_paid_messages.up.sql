-- Paid private messages (https://core.telegram.org/api/paid-messages).
-- One row per paid logical private message: what the sender paid, what the
-- recipient was credited after commission, and whether it was refunded.
CREATE TABLE public.private_paid_messages (
    payer_user_id bigint NOT NULL,
    private_message_id bigint NOT NULL,
    recipient_user_id bigint NOT NULL,
    stars bigint NOT NULL CHECK (stars > 0),
    credited bigint NOT NULL CHECK (credited >= 0 AND credited <= stars),
    date integer NOT NULL,
    refunded_at integer,
    PRIMARY KEY (payer_user_id, private_message_id)
);

CREATE INDEX private_paid_messages_open_pair_idx
    ON public.private_paid_messages (recipient_user_id, payer_user_id)
    WHERE refunded_at IS NULL;
