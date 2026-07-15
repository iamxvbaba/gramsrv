DROP TABLE IF EXISTS public.auth_key_session_layers;

ALTER TABLE public.auth_keys
    DROP CONSTRAINT IF EXISTS auth_keys_layer_observation_id_valid,
    DROP COLUMN IF EXISTS layer_observation_id;

DROP SEQUENCE IF EXISTS public.auth_key_layer_observation_seq;
