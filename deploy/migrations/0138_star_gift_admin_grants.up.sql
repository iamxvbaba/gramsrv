-- Direct admin collectible grants are one idempotent aggregate: unique
-- issuance, saved ownership, private message, pts/outbox and this receipt.
CREATE TABLE public.star_gift_admin_grant_commands (
    recipient_user_id bigint NOT NULL,
    command_key text NOT NULL,
    request_fingerprint bytea NOT NULL,
    sender_user_id bigint NOT NULL,
    gift_id bigint NOT NULL,
    saved_gift_id bigint NOT NULL REFERENCES public.peer_star_gifts(id) ON DELETE RESTRICT,
    unique_gift_id bigint NOT NULL REFERENCES public.unique_star_gifts(id) ON DELETE RESTRICT,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT star_gift_admin_grant_commands_pkey PRIMARY KEY (recipient_user_id, command_key),
    CONSTRAINT star_gift_admin_grant_command_saved_uniq UNIQUE (saved_gift_id),
    CONSTRAINT star_gift_admin_grant_command_unique_uniq UNIQUE (unique_gift_id),
    CONSTRAINT star_gift_admin_grant_command_shape_check CHECK (
        recipient_user_id > 0
        AND sender_user_id = 777000
        AND gift_id > 0
        AND char_length(command_key) BETWEEN 1 AND 256
        AND octet_length(request_fingerprint) = 32
    )
);
