-- Short-lived, durable per-session invokeWithLayer watermark. The in-process
-- exact-profile registry prevents ordinary replay rollback, while this table
-- closes the durable commit/restart and cross-instance replacement-connection
-- window for client msg_ids that are still fresh enough to be accepted by
-- MTProto. It does not broadcast profile changes into an already-live remote
-- physical connection; that connection remains frozen until its own selector.
CREATE SEQUENCE public.auth_key_layer_observation_seq AS bigint;

ALTER TABLE public.auth_keys
    ADD COLUMN layer_observation_id bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT auth_keys_layer_observation_id_valid
        CHECK (layer_observation_id >= 0);

CREATE TABLE public.auth_key_session_layers (
    raw_auth_key_id bigint NOT NULL,
    session_id bigint NOT NULL,
    layer integer NOT NULL,
    msg_id bigint NOT NULL,
    observation_id bigint NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (raw_auth_key_id, session_id),
    CONSTRAINT auth_key_session_layers_auth_key_fkey
        FOREIGN KEY (raw_auth_key_id)
        REFERENCES public.auth_keys(auth_key_id)
        ON DELETE CASCADE,
    CONSTRAINT auth_key_session_layers_layer_valid CHECK (layer > 0),
    CONSTRAINT auth_key_session_layers_msg_id_valid
        CHECK (
            msg_id > 0
            AND msg_id % 4 = 0
            AND (msg_id & 4294967295) <> 0
        ),
    CONSTRAINT auth_key_session_layers_observation_id_valid
        CHECK (observation_id > 0)
);

CREATE INDEX auth_key_session_layers_expiry_idx
    ON public.auth_key_session_layers (expires_at, raw_auth_key_id, session_id);
