-- Complete collectible Star Gift lifecycle: ownership state, transfer/resale, purchase
-- offers, crafting, auctions, notification preferences and the explicit TON boundary.

ALTER TABLE public.peer_star_gifts
    DROP CONSTRAINT IF EXISTS peer_star_gifts_terminal_state_check,
    ADD COLUMN lifecycle_status text DEFAULT 'active' NOT NULL,
    ADD COLUMN transfer_stars bigint DEFAULT 0 NOT NULL,
	ADD COLUMN prepaid_upgrade_hash text DEFAULT '' NOT NULL,
	ADD COLUMN gift_num integer DEFAULT 0 NOT NULL,
    ADD COLUMN can_export_at integer DEFAULT 0 NOT NULL,
    ADD COLUMN can_transfer_at integer DEFAULT 0 NOT NULL,
    ADD COLUMN can_resell_at integer DEFAULT 0 NOT NULL,
    ADD COLUMN drop_original_details_stars bigint DEFAULT 0 NOT NULL,
    ADD COLUMN can_craft_at integer DEFAULT 0 NOT NULL;

UPDATE public.peer_star_gifts SET lifecycle_status='converted' WHERE converted;

ALTER TABLE public.peer_star_gifts
    ADD CONSTRAINT peer_star_gifts_lifecycle_check CHECK (
        lifecycle_status IN ('active', 'converted', 'burned', 'exported') AND
        transfer_stars >= 0 AND gift_num >= 0 AND can_export_at >= 0 AND can_transfer_at >= 0 AND
        can_resell_at >= 0 AND drop_original_details_stars >= 0 AND can_craft_at >= 0 AND
        ((lifecycle_status='converted' AND converted AND unique_gift_id IS NULL) OR
         (lifecycle_status='active' AND NOT converted) OR
         (lifecycle_status IN ('burned','exported') AND NOT converted AND unique_gift_id IS NOT NULL))
    );

CREATE UNIQUE INDEX peer_star_gifts_prepaid_upgrade_hash_uniq
    ON public.peer_star_gifts(prepaid_upgrade_hash) WHERE prepaid_upgrade_hash<>'';

ALTER TABLE public.unique_star_gifts
    ALTER COLUMN owner_peer_type DROP NOT NULL,
    ALTER COLUMN owner_peer_id DROP NOT NULL,
    DROP CONSTRAINT IF EXISTS unique_star_gift_owner_check,
    ADD COLUMN require_premium boolean DEFAULT false NOT NULL,
    ADD COLUMN resale_ton_only boolean DEFAULT false NOT NULL,
    ADD COLUMN theme_available boolean DEFAULT false NOT NULL,
    ADD COLUMN burned boolean DEFAULT false NOT NULL,
    ADD COLUMN crafted boolean DEFAULT false NOT NULL,
    ADD COLUMN original_owner_peer_type text,
    ADD COLUMN original_owner_peer_id bigint,
    ADD COLUMN owner_name text DEFAULT '' NOT NULL,
    ADD COLUMN owner_address text DEFAULT '' NOT NULL,
    ADD COLUMN gift_address text DEFAULT '' NOT NULL,
    ADD COLUMN released_by_peer_type text,
    ADD COLUMN released_by_peer_id bigint,
    ADD COLUMN value_amount bigint DEFAULT 0 NOT NULL,
    ADD COLUMN value_currency text DEFAULT '' NOT NULL,
    ADD COLUMN value_usd_amount bigint DEFAULT 0 NOT NULL,
    ADD COLUMN theme_peer_type text,
    ADD COLUMN theme_peer_id bigint,
    ADD COLUMN host_peer_type text,
    ADD COLUMN host_peer_id bigint,
    ADD COLUMN offer_min_stars integer DEFAULT 0 NOT NULL,
    ADD COLUMN craft_chance_permille integer DEFAULT 0 NOT NULL,
    ADD COLUMN last_sale_date integer DEFAULT 0 NOT NULL,
    ADD COLUMN last_sale_currency text DEFAULT '' NOT NULL,
    ADD COLUMN last_sale_amount bigint DEFAULT 0 NOT NULL,
    ADD CONSTRAINT unique_star_gift_owner_check CHECK (
        (owner_peer_type IN ('user','channel') AND owner_peer_id > 0 AND owner_address='') OR
        (owner_peer_type IS NULL AND owner_peer_id IS NULL AND owner_address<>'')
    ),
    ADD CONSTRAINT unique_star_gift_released_by_check CHECK (
        (released_by_peer_type IS NULL AND released_by_peer_id IS NULL) OR
        (released_by_peer_type IN ('user','channel') AND released_by_peer_id > 0)
    ),
    ADD CONSTRAINT unique_star_gift_theme_peer_check CHECK (
        (theme_peer_type IS NULL AND theme_peer_id IS NULL) OR
        (theme_peer_type IN ('user','channel') AND theme_peer_id > 0)
    ),
    ADD CONSTRAINT unique_star_gift_host_peer_check CHECK (
        (host_peer_type IS NULL AND host_peer_id IS NULL) OR
        (host_peer_type IN ('user','channel') AND host_peer_id > 0)
    ),
    ADD CONSTRAINT unique_star_gift_value_check CHECK (
        value_amount >= 0 AND value_usd_amount >= 0 AND offer_min_stars >= 0 AND
        craft_chance_permille BETWEEN 0 AND 1000 AND last_sale_date >= 0 AND last_sale_amount >= 0 AND
        ((value_currency='' AND value_amount=0) OR value_currency<>'') AND
        ((last_sale_currency='' AND last_sale_amount=0 AND last_sale_date=0) OR
         (last_sale_currency IN ('XTR','TON') AND last_sale_amount>0 AND last_sale_date>0)) AND
        (NOT burned OR owner_address='') AND
        ((owner_address='' AND gift_address='') OR (owner_address<>'' AND gift_address<>''))
    );

UPDATE public.unique_star_gifts u
SET original_owner_peer_type=p.owner_peer_type, original_owner_peer_id=p.owner_peer_id
FROM public.peer_star_gifts p WHERE p.id=u.source_saved_gift_id;

ALTER TABLE public.unique_star_gifts
    ALTER COLUMN original_owner_peer_type SET NOT NULL,
    ALTER COLUMN original_owner_peer_id SET NOT NULL,
    ADD CONSTRAINT unique_star_gift_original_owner_check CHECK (
        original_owner_peer_type IN ('user','channel') AND original_owner_peer_id>0
    );

CREATE TABLE public.star_gift_listings (
    unique_gift_id bigint PRIMARY KEY REFERENCES public.unique_star_gifts(id) ON DELETE RESTRICT,
    seller_peer_type text NOT NULL,
    seller_peer_id bigint NOT NULL,
    currency text NOT NULL,
    amount bigint NOT NULL,
    listed_at integer NOT NULL,
    updated_at integer NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT star_gift_listing_seller_check CHECK (seller_peer_type IN ('user','channel') AND seller_peer_id>0),
    CONSTRAINT star_gift_listing_amount_check CHECK (currency IN ('XTR','TON') AND amount>0 AND listed_at>0 AND updated_at>=listed_at)
);
CREATE INDEX star_gift_listings_gift_price_idx ON public.star_gift_listings(currency, amount, unique_gift_id);
CREATE INDEX star_gift_listings_updated_idx ON public.star_gift_listings(updated_at DESC, unique_gift_id DESC);

CREATE TABLE public.star_gift_sales (
    id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    unique_gift_id bigint NOT NULL REFERENCES public.unique_star_gifts(id) ON DELETE RESTRICT,
    seller_peer_type text NOT NULL,
    seller_peer_id bigint NOT NULL,
    buyer_peer_type text NOT NULL,
    buyer_peer_id bigint NOT NULL,
    currency text NOT NULL,
    amount bigint NOT NULL,
    commission_amount bigint DEFAULT 0 NOT NULL,
    sold_at integer NOT NULL,
    command_key text NOT NULL,
    CONSTRAINT star_gift_sales_command_uniq UNIQUE(command_key),
    CONSTRAINT star_gift_sales_peer_check CHECK (
        seller_peer_type IN ('user','channel') AND seller_peer_id>0 AND
        buyer_peer_type IN ('user','channel') AND buyer_peer_id>0),
    CONSTRAINT star_gift_sales_amount_check CHECK (
        currency IN ('XTR','TON') AND amount>0 AND commission_amount>=0 AND commission_amount<=amount AND sold_at>0)
);
CREATE INDEX star_gift_sales_unique_date_idx ON public.star_gift_sales(unique_gift_id, sold_at DESC);

CREATE TABLE public.star_gift_offers (
    id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    buyer_user_id bigint NOT NULL,
    owner_peer_type text NOT NULL,
    owner_peer_id bigint NOT NULL,
    unique_gift_id bigint NOT NULL REFERENCES public.unique_star_gifts(id) ON DELETE RESTRICT,
    currency text NOT NULL,
    amount bigint NOT NULL,
    random_id bigint NOT NULL,
    offer_msg_id integer DEFAULT 0 NOT NULL,
    buyer_msg_id integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    created_at integer NOT NULL,
    expires_at integer NOT NULL,
    resolved_at integer DEFAULT 0 NOT NULL,
    balance_after bigint DEFAULT 0 NOT NULL,
	expiry_notified boolean DEFAULT false NOT NULL,
    CONSTRAINT star_gift_offer_random_uniq UNIQUE(buyer_user_id, random_id),
    CONSTRAINT star_gift_offer_owner_msg_uniq UNIQUE(owner_peer_type, owner_peer_id, offer_msg_id),
    CONSTRAINT star_gift_offer_peer_check CHECK (buyer_user_id>0 AND owner_peer_type IN ('user','channel') AND owner_peer_id>0),
    CONSTRAINT star_gift_offer_amount_check CHECK (currency IN ('XTR','TON') AND amount>0),
    CONSTRAINT star_gift_offer_status_check CHECK (status IN ('pending','accepted','declined','expired','cancelled')),
    CONSTRAINT star_gift_offer_time_check CHECK (created_at>0 AND expires_at>created_at AND resolved_at>=0 AND
        ((status='pending' AND resolved_at=0) OR (status<>'pending' AND resolved_at>=created_at)))
);
CREATE INDEX star_gift_offers_pending_expiry_idx ON public.star_gift_offers(expires_at, id) WHERE status='pending';
CREATE INDEX star_gift_offers_unique_pending_idx ON public.star_gift_offers(unique_gift_id, id) WHERE status='pending';

CREATE TABLE public.star_gift_transfer_commands (
    actor_user_id bigint NOT NULL,
    command_key text NOT NULL,
    unique_gift_id bigint NOT NULL REFERENCES public.unique_star_gifts(id) ON DELETE RESTRICT,
    from_peer_type text NOT NULL,
    from_peer_id bigint NOT NULL,
    to_peer_type text NOT NULL,
    to_peer_id bigint NOT NULL,
    charge_stars bigint DEFAULT 0 NOT NULL,
    balance_after bigint DEFAULT 0 NOT NULL,
    created_at integer NOT NULL,
    CONSTRAINT star_gift_transfer_commands_pkey PRIMARY KEY(actor_user_id, command_key),
    CONSTRAINT star_gift_transfer_command_peer_check CHECK (
        actor_user_id>0 AND from_peer_type IN ('user','channel') AND from_peer_id>0 AND
        to_peer_type IN ('user','channel') AND to_peer_id>0 AND charge_stars>=0 AND created_at>0)
);

CREATE TABLE public.star_gift_purchase_commands (
    buyer_user_id bigint NOT NULL,
    command_key text NOT NULL,
    gift_id bigint NOT NULL REFERENCES public.star_gift_catalog(gift_id) ON DELETE RESTRICT,
    recipient_peer_type text NOT NULL,
    recipient_peer_id bigint NOT NULL,
    saved_gift_id bigint NOT NULL REFERENCES public.peer_star_gifts(id) ON DELETE RESTRICT,
    form_id bigint NOT NULL,
    charge_stars bigint NOT NULL,
    balance_after bigint NOT NULL,
    created_at integer NOT NULL,
    CONSTRAINT star_gift_purchase_commands_pkey PRIMARY KEY(buyer_user_id,command_key),
    CONSTRAINT star_gift_purchase_commands_form_uniq UNIQUE(buyer_user_id,form_id),
    CONSTRAINT star_gift_purchase_command_shape_check CHECK (
        buyer_user_id>0 AND recipient_peer_type IN ('user','channel') AND recipient_peer_id>0 AND
        form_id>0 AND charge_stars>0 AND balance_after>=0 AND created_at>0)
);

CREATE TABLE public.star_gift_prepaid_upgrade_commands (
    payer_user_id bigint NOT NULL,
    command_key text NOT NULL,
    saved_gift_id bigint NOT NULL REFERENCES public.peer_star_gifts(id) ON DELETE RESTRICT,
    form_id bigint NOT NULL,
    charge_stars bigint NOT NULL,
    balance_after bigint NOT NULL,
    created_at integer NOT NULL,
    CONSTRAINT star_gift_prepaid_upgrade_commands_pkey PRIMARY KEY(payer_user_id, command_key),
    CONSTRAINT star_gift_prepaid_upgrade_commands_form_uniq UNIQUE(payer_user_id, form_id),
    CONSTRAINT star_gift_prepaid_upgrade_command_shape_check CHECK (
        payer_user_id>0 AND form_id>0 AND charge_stars>0 AND balance_after>=0 AND created_at>0)
);

CREATE TABLE public.star_gift_drop_details_commands (
    user_id bigint NOT NULL,
    command_key text NOT NULL,
    saved_gift_id bigint NOT NULL REFERENCES public.peer_star_gifts(id) ON DELETE RESTRICT,
    unique_gift_id bigint NOT NULL REFERENCES public.unique_star_gifts(id) ON DELETE RESTRICT,
    form_id bigint NOT NULL,
    charge_stars bigint NOT NULL,
    balance_after bigint NOT NULL,
    created_at integer NOT NULL,
    CONSTRAINT star_gift_drop_details_commands_pkey PRIMARY KEY(user_id, command_key),
    CONSTRAINT star_gift_drop_details_commands_form_uniq UNIQUE(user_id, form_id),
    CONSTRAINT star_gift_drop_details_command_shape_check CHECK (
        user_id>0 AND form_id>0 AND charge_stars>0 AND balance_after>=0 AND created_at>0)
);

CREATE TABLE public.star_gift_craft_commands (
    user_id bigint NOT NULL,
    command_key text NOT NULL,
    input_unique_gift_ids bigint[] NOT NULL,
    gift_id bigint NOT NULL REFERENCES public.star_gift_catalog(gift_id) ON DELETE RESTRICT,
    success boolean NOT NULL,
    result_unique_gift_id bigint REFERENCES public.unique_star_gifts(id) ON DELETE RESTRICT,
    chance_permille integer NOT NULL,
    created_at integer NOT NULL,
    CONSTRAINT star_gift_craft_commands_pkey PRIMARY KEY(user_id, command_key),
    CONSTRAINT star_gift_craft_shape_check CHECK (
        user_id>0 AND cardinality(input_unique_gift_ids) BETWEEN 1 AND 4 AND
        chance_permille BETWEEN 0 AND 1000 AND created_at>0 AND
        ((success AND result_unique_gift_id IS NOT NULL) OR (NOT success AND result_unique_gift_id IS NULL)))
);

CREATE TABLE public.star_gift_notification_settings (
    user_id bigint NOT NULL,
    channel_id bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT star_gift_notification_settings_pkey PRIMARY KEY(user_id, channel_id),
    CONSTRAINT star_gift_notification_settings_peer_check CHECK (user_id>0 AND channel_id>0)
);

CREATE TABLE public.star_gift_withdrawal_requests (
    id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    unique_gift_id bigint NOT NULL REFERENCES public.unique_star_gifts(id) ON DELETE RESTRICT,
    owner_user_id bigint NOT NULL,
    provider text NOT NULL,
    provider_request_id text NOT NULL,
    url text NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    created_at integer NOT NULL,
    expires_at integer NOT NULL,
    completed_at integer DEFAULT 0 NOT NULL,
    CONSTRAINT star_gift_withdrawal_request_unique UNIQUE(unique_gift_id),
    CONSTRAINT star_gift_withdrawal_provider_request_uniq UNIQUE(provider, provider_request_id),
    CONSTRAINT star_gift_withdrawal_shape_check CHECK (
        owner_user_id>0 AND provider<>'' AND provider_request_id<>'' AND url<>'' AND
        status IN ('pending','completed','failed') AND created_at>0 AND expires_at>created_at AND completed_at>=0)
);

CREATE TABLE public.star_gift_auctions (
    gift_id bigint PRIMARY KEY REFERENCES public.star_gift_catalog(gift_id) ON DELETE RESTRICT,
    slug text NOT NULL UNIQUE,
    version integer DEFAULT 1 NOT NULL,
    start_date integer NOT NULL,
    end_date integer NOT NULL,
    round_duration integer NOT NULL,
    gifts_per_round integer NOT NULL,
    total_rounds integer NOT NULL,
    current_round integer DEFAULT 0 NOT NULL,
    next_round_at integer NOT NULL,
    last_gift_num integer DEFAULT 0 NOT NULL,
    gifts_left integer NOT NULL,
    min_bid_amount bigint NOT NULL,
    status text DEFAULT 'pending' NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT star_gift_auction_shape_check CHECK (
        version>0 AND start_date>0 AND end_date>start_date AND round_duration>0 AND
        gifts_per_round>0 AND total_rounds>0 AND current_round BETWEEN 0 AND total_rounds AND
        next_round_at>=start_date AND last_gift_num>=0 AND gifts_left>=0 AND min_bid_amount>0 AND
        status IN ('pending','active','completed','cancelled'))
);

CREATE TABLE public.star_gift_auction_bids (
    gift_id bigint NOT NULL REFERENCES public.star_gift_auctions(gift_id) ON DELETE RESTRICT,
    bidder_user_id bigint NOT NULL,
    recipient_peer_type text NOT NULL,
    recipient_peer_id bigint NOT NULL,
    amount bigint NOT NULL,
    bid_date integer NOT NULL,
    hide_name boolean DEFAULT false NOT NULL,
    message text DEFAULT '' NOT NULL,
    returned boolean DEFAULT false NOT NULL,
    acquired_count integer DEFAULT 0 NOT NULL,
	active boolean DEFAULT true NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT star_gift_auction_bids_pkey PRIMARY KEY(gift_id, bidder_user_id),
    CONSTRAINT star_gift_auction_bid_peer_check CHECK (
        bidder_user_id>0 AND recipient_peer_type IN ('user','channel') AND recipient_peer_id>0),
    CONSTRAINT star_gift_auction_bid_amount_check CHECK (amount>0 AND bid_date>0 AND acquired_count>=0 AND version>0)
);
CREATE INDEX star_gift_auction_bids_rank_idx ON public.star_gift_auction_bids(gift_id, amount DESC, bid_date, bidder_user_id) WHERE active;

CREATE TABLE public.star_gift_auction_bid_payments (
    user_id bigint NOT NULL,
    form_id bigint NOT NULL,
    gift_id bigint NOT NULL REFERENCES public.star_gift_auctions(gift_id) ON DELETE RESTRICT,
    bid_amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    created_at integer NOT NULL,
    CONSTRAINT star_gift_auction_bid_payments_pkey PRIMARY KEY(user_id, form_id),
    CONSTRAINT star_gift_auction_bid_payment_shape_check CHECK (
        user_id>0 AND form_id>0 AND bid_amount>0 AND balance_after>=0 AND created_at>0)
);

CREATE TABLE public.star_gift_auction_acquired (
    id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    gift_id bigint NOT NULL REFERENCES public.star_gift_auctions(gift_id) ON DELETE RESTRICT,
    bidder_user_id bigint NOT NULL,
    recipient_peer_type text NOT NULL,
    recipient_peer_id bigint NOT NULL,
    saved_gift_id bigint REFERENCES public.peer_star_gifts(id) ON DELETE RESTRICT,
    bid_amount bigint NOT NULL,
    round integer NOT NULL,
    pos integer NOT NULL,
    gift_num integer,
    acquired_at integer NOT NULL,
    hide_name boolean DEFAULT false NOT NULL,
    message text DEFAULT '' NOT NULL,
    CONSTRAINT star_gift_auction_acquired_round_pos_uniq UNIQUE(gift_id, round, pos),
    CONSTRAINT star_gift_auction_acquired_shape_check CHECK (
        bidder_user_id>0 AND recipient_peer_type IN ('user','channel') AND recipient_peer_id>0 AND
        bid_amount>0 AND round>0 AND pos>0 AND acquired_at>0 AND (gift_num IS NULL OR gift_num>0))
);
CREATE INDEX star_gift_auction_acquired_user_idx ON public.star_gift_auction_acquired(bidder_user_id, gift_id, id);

CREATE TABLE public.ton_balances (
    user_id bigint PRIMARY KEY,
    balance_nanoton bigint DEFAULT 0 NOT NULL,
    granted boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ton_balances_check CHECK (user_id>0 AND balance_nanoton>=0)
);

CREATE TABLE public.ton_transactions (
    id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL,
    amount_nanoton bigint NOT NULL,
    reason text NOT NULL,
    peer_type text,
    peer_id bigint,
    gift_id bigint,
    date integer NOT NULL,
    CONSTRAINT ton_transaction_amount_check CHECK (user_id>0 AND amount_nanoton<>0 AND date>0),
    CONSTRAINT ton_transaction_peer_check CHECK (
        (peer_type IS NULL AND peer_id IS NULL) OR (peer_type IN ('user','channel') AND peer_id>0))
);
CREATE INDEX ton_transactions_user_idx ON public.ton_transactions(user_id, id DESC);

CREATE FUNCTION public.telesrv_guard_star_gift_listing() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    gift_owner_type text;
    gift_owner_id bigint;
    gift_burned boolean;
BEGIN
    SELECT owner_peer_type, owner_peer_id, burned
      INTO gift_owner_type, gift_owner_id, gift_burned
      FROM public.unique_star_gifts WHERE id=NEW.unique_gift_id FOR SHARE;
    IF gift_burned OR gift_owner_type IS DISTINCT FROM NEW.seller_peer_type OR gift_owner_id IS DISTINCT FROM NEW.seller_peer_id THEN
        RAISE EXCEPTION 'star gift listing owner/state mismatch';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER star_gift_listing_guard BEFORE INSERT OR UPDATE ON public.star_gift_listings
    FOR EACH ROW EXECUTE FUNCTION public.telesrv_guard_star_gift_listing();

CREATE FUNCTION public.telesrv_check_unique_star_gift_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    unique_id bigint;
    gift_owner_type text;
    gift_owner_id bigint;
    gift_owner_address text;
    gift_burned boolean;
    saved_status text;
    saved_owner_type text;
    saved_owner_id bigint;
BEGIN
    IF TG_TABLE_NAME = 'unique_star_gifts' THEN
        unique_id := COALESCE(NEW.id, OLD.id);
    ELSE
        unique_id := COALESCE(NEW.unique_gift_id, OLD.unique_gift_id);
    END IF;
    IF unique_id IS NULL THEN RETURN NULL; END IF;
    SELECT owner_peer_type, owner_peer_id, owner_address, burned
      INTO gift_owner_type, gift_owner_id, gift_owner_address, gift_burned
      FROM public.unique_star_gifts WHERE id=unique_id;
    IF NOT FOUND THEN RETURN NULL; END IF;
    SELECT lifecycle_status, owner_peer_type, owner_peer_id
      INTO saved_status, saved_owner_type, saved_owner_id
      FROM public.peer_star_gifts WHERE unique_gift_id=unique_id;
    IF NOT FOUND THEN RAISE EXCEPTION 'unique star gift missing saved aggregate'; END IF;
    IF gift_burned THEN
        IF saved_status <> 'burned' THEN RAISE EXCEPTION 'burned unique star gift has live saved aggregate'; END IF;
    ELSIF gift_owner_address <> '' THEN
        IF saved_status <> 'exported' THEN RAISE EXCEPTION 'exported unique star gift has non-exported saved aggregate'; END IF;
    ELSIF saved_status <> 'active' OR gift_owner_type IS DISTINCT FROM saved_owner_type OR gift_owner_id IS DISTINCT FROM saved_owner_id THEN
        RAISE EXCEPTION 'unique star gift owner mismatch';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER unique_star_gift_owner_guard
    AFTER INSERT OR UPDATE ON public.unique_star_gifts DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.telesrv_check_unique_star_gift_owner();
CREATE CONSTRAINT TRIGGER peer_unique_star_gift_owner_guard
    AFTER INSERT OR UPDATE ON public.peer_star_gifts DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (NEW.unique_gift_id IS NOT NULL)
    EXECUTE FUNCTION public.telesrv_check_unique_star_gift_owner();
