-- Durable suggested-post approval/payment/publication state.  The row is the
-- idempotency key for a monoforum suggestion; message/update rows remain the
-- client-visible source of truth and are written in the same transaction.
CREATE TABLE public.suggested_post_approvals (
    monoforum_id bigint NOT NULL,
    suggestion_message_id integer NOT NULL,
    parent_channel_id bigint NOT NULL,
    actor_user_id bigint NOT NULL,
    payer_user_id bigint NOT NULL,
    state text NOT NULL,
    price_kind text NOT NULL DEFAULT '',
    price_amount bigint NOT NULL DEFAULT 0,
    price_nanos integer NOT NULL DEFAULT 0,
    schedule_date integer NOT NULL DEFAULT 0,
    approval_service_message_id integer NOT NULL DEFAULT 0,
    published_message_id integer NOT NULL DEFAULT 0,
    settlement_due integer NOT NULL DEFAULT 0,
    final_service_message_id integer NOT NULL DEFAULT 0,
    created_at integer NOT NULL,
    updated_at integer NOT NULL,
    PRIMARY KEY (monoforum_id, suggestion_message_id),
    CONSTRAINT suggested_post_approvals_shape_check CHECK (
        monoforum_id>0 AND suggestion_message_id>0 AND parent_channel_id>0 AND
        actor_user_id>0 AND payer_user_id>0 AND created_at>0 AND updated_at>=created_at AND
        state IN ('balance_low','rejected','scheduled','published','completed','refunded') AND
        price_kind IN ('','stars','ton') AND price_amount>=0 AND price_nanos BETWEEN 0 AND 999999999 AND
        ((price_kind='' AND price_amount=0 AND price_nanos=0) OR
         (price_kind='stars' AND price_amount>0) OR
         (price_kind='ton' AND price_amount>0 AND price_nanos=0)) AND
        schedule_date>=0 AND approval_service_message_id>=0 AND published_message_id>=0 AND
        settlement_due>=0 AND final_service_message_id>=0)
);

CREATE INDEX suggested_post_approvals_schedule_idx
    ON public.suggested_post_approvals(schedule_date,monoforum_id,suggestion_message_id)
    WHERE state='scheduled';
CREATE INDEX suggested_post_approvals_settlement_idx
    ON public.suggested_post_approvals(settlement_due,monoforum_id,suggestion_message_id)
    WHERE state='published';
