-- The data backfill is intentionally retained on rollback.  Restore only the
-- pre-0134 shape constraint, which allowed zero schedule_date in every state.
ALTER TABLE suggested_post_approvals
    DROP CONSTRAINT suggested_post_approvals_shape_check;

ALTER TABLE suggested_post_approvals
    ADD CONSTRAINT suggested_post_approvals_shape_check CHECK (
        monoforum_id>0 AND suggestion_message_id>0 AND parent_channel_id>0 AND
        actor_user_id>0 AND payer_user_id>0 AND created_at>0 AND updated_at>=created_at AND
        state IN ('balance_low','rejected','scheduled','published','completed','refunded') AND
        price_kind IN ('','stars','ton') AND price_amount>=0 AND price_nanos BETWEEN 0 AND 999999999 AND
        ((price_kind='' AND price_amount=0 AND price_nanos=0) OR
         (price_kind='stars' AND price_amount>0) OR
         (price_kind='ton' AND price_amount>0 AND price_nanos=0)) AND
        schedule_date>=0 AND approval_service_message_id>=0 AND published_message_id>=0 AND
        settlement_due>=0 AND final_service_message_id>=0
    );
