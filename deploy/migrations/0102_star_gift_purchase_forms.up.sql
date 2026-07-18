CREATE TABLE public.star_gift_purchase_forms (
    buyer_user_id bigint NOT NULL,
    form_id bigint NOT NULL,
    gift_id bigint NOT NULL REFERENCES public.star_gift_catalog(gift_id) ON DELETE RESTRICT,
    revision_id bigint NOT NULL REFERENCES public.star_gift_catalog_revisions(id) ON DELETE RESTRICT,
    recipient_peer_type text NOT NULL,
    recipient_peer_id bigint NOT NULL,
    include_upgrade boolean DEFAULT false NOT NULL,
    hide_name boolean DEFAULT false NOT NULL,
    message text DEFAULT '' NOT NULL,
    charge_stars bigint NOT NULL,
    issued_at integer NOT NULL,
    expires_at integer NOT NULL,
    CONSTRAINT star_gift_purchase_forms_pkey PRIMARY KEY (buyer_user_id, form_id),
    CONSTRAINT star_gift_purchase_form_shape_check CHECK (
        buyer_user_id > 0 AND form_id <> 0 AND gift_id > 0 AND revision_id > 0 AND
        recipient_peer_type IN ('user', 'channel') AND recipient_peer_id > 0 AND
        charge_stars > 0 AND issued_at > 0 AND expires_at = issued_at + 600 AND
        char_length(message) <= 128)
);

CREATE INDEX star_gift_purchase_forms_expiry_idx
    ON public.star_gift_purchase_forms (expires_at, buyer_user_id, form_id);
