ALTER TABLE public.star_gift_purchase_commands
    DROP CONSTRAINT star_gift_purchase_command_shape_check,
    ADD CONSTRAINT star_gift_purchase_command_shape_check CHECK (
        buyer_user_id>0 AND recipient_peer_type IN ('user','channel') AND recipient_peer_id>0 AND
        form_id<>0 AND charge_stars>0 AND balance_after>=0 AND created_at>0);

ALTER TABLE public.star_gift_prepaid_upgrade_commands
    DROP CONSTRAINT star_gift_prepaid_upgrade_command_shape_check,
    ADD CONSTRAINT star_gift_prepaid_upgrade_command_shape_check CHECK (
        payer_user_id>0 AND form_id<>0 AND charge_stars>0 AND balance_after>=0 AND created_at>0);

ALTER TABLE public.star_gift_drop_details_commands
    DROP CONSTRAINT star_gift_drop_details_command_shape_check,
    ADD CONSTRAINT star_gift_drop_details_command_shape_check CHECK (
        user_id>0 AND form_id<>0 AND charge_stars>0 AND balance_after>=0 AND created_at>0);

ALTER TABLE public.star_gift_auction_bid_payments
    DROP CONSTRAINT star_gift_auction_bid_payment_shape_check,
    ADD CONSTRAINT star_gift_auction_bid_payment_shape_check CHECK (
        user_id>0 AND form_id<>0 AND bid_amount>0 AND balance_after>=0 AND created_at>0);
