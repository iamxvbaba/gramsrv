-- Preserve the protocol key kind/lifetime established by p_q_inner_data(_temp).
-- A temporary key must expire at the MTProto edge; it must never fall through
-- to the RPC router and be mistaken for an independent permanent identity.
ALTER TABLE public.auth_keys
    ADD COLUMN expires_at integer NOT NULL DEFAULT -1;

ALTER TABLE public.auth_keys
    ADD CONSTRAINT auth_keys_expires_at_valid CHECK (expires_at >= -1);

-- auth_keys.expires_at is the only protocol-lifetime fact. Retention seeks this
-- partial index so unbound temporary handshakes and bound keys follow the same
-- bounded cleanup path; the binding-side expiry index remains only for legacy
-- rollback compatibility.
CREATE INDEX auth_keys_temporary_expiry_seek_idx
    ON public.auth_keys (expires_at, auth_key_id)
    WHERE expires_at > 0;

-- Existing bound temporary keys are unambiguous and can be backfilled from the
-- durable bind record. Unclassified historical keys remain -1 and are rejected
-- once with protocol -404 after restart, forcing a clean handshake instead of
-- guessing that they are permanent. New handshakes always write 0 (permanent)
-- or their positive absolute expiry before dh_gen_ok.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.temp_auth_key_bindings
        WHERE expires_at <= 0
    ) THEN
        RAISE EXCEPTION 'invalid non-positive temporary auth key expiry; repair before migration 0086';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM public.temp_auth_key_bindings AS b
        LEFT JOIN public.auth_keys AS k ON k.auth_key_id = b.perm_auth_key_id
        WHERE k.auth_key_id IS NULL
    ) THEN
        RAISE EXCEPTION 'temporary auth key binding references missing permanent key; repair before migration 0086';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM public.temp_auth_key_bindings
        WHERE temp_auth_key_id = perm_auth_key_id
    ) THEN
        RAISE EXCEPTION 'temporary auth key self-binding; repair before migration 0086';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM public.temp_auth_key_bindings AS temp_role
        JOIN public.temp_auth_key_bindings AS perm_role
          ON perm_role.perm_auth_key_id = temp_role.temp_auth_key_id
    ) THEN
        RAISE EXCEPTION 'auth key appears in both temporary and permanent roles; repair before migration 0086';
    END IF;
END
$$;

UPDATE public.auth_keys AS k
SET expires_at = b.expires_at
FROM public.temp_auth_key_bindings AS b
WHERE k.auth_key_id = b.temp_auth_key_id;

-- Do not normalize an early telesrv bug during reads. If a deployment contains
-- an authorization written against a bound temp key, stop the migration and
-- require an explicit data repair after inspecting the corresponding perm key.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.authorizations AS a
        JOIN public.temp_auth_key_bindings AS b
          ON b.temp_auth_key_id = a.auth_key_id
    ) THEN
        RAISE EXCEPTION 'invalid authorization on temporary auth key; repair before migration 0086';
    END IF;
END
$$;

-- Every durable table keyed by business/device auth identity must also be free
-- of bound temporary IDs. These tables intentionally do not FK to auth_keys
-- because several retain historical delivery facts; silently deleting the key
-- would therefore strand an identity split instead of repairing it. The
-- physical-session exclusion tuple in dispatch_outbox is deliberately omitted:
-- it stores raw auth_key_id + session_id and a temporary raw key is valid there.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM public.update_states AS s
        JOIN public.temp_auth_key_bindings AS b ON b.temp_auth_key_id = s.auth_key_id
    ) THEN
        RAISE EXCEPTION 'update state references temporary auth key; repair before migration 0086';
    END IF;
    IF EXISTS (
        SELECT 1 FROM public.bootstrap_update_jobs AS j
        JOIN public.temp_auth_key_bindings AS b ON b.temp_auth_key_id = j.auth_key_id
    ) THEN
        RAISE EXCEPTION 'bootstrap update job references temporary auth key; repair before migration 0086';
    END IF;
    IF EXISTS (
        SELECT 1 FROM public.secret_qts_watermarks AS q
        JOIN public.temp_auth_key_bindings AS b ON b.temp_auth_key_id = q.auth_key_id
    ) THEN
        RAISE EXCEPTION 'secret qts watermark references temporary auth key; repair before migration 0086';
    END IF;
    IF EXISTS (
        SELECT 1 FROM public.encrypted_message_queue AS q
        JOIN public.temp_auth_key_bindings AS b ON b.temp_auth_key_id = q.receiver_auth_key_id
    ) THEN
        RAISE EXCEPTION 'encrypted message queue references temporary auth key; repair before migration 0086';
    END IF;
    IF EXISTS (
        SELECT 1 FROM public.encrypted_state_event_delivery AS d
        JOIN public.temp_auth_key_bindings AS b ON b.temp_auth_key_id = d.auth_key_id
    ) THEN
        RAISE EXCEPTION 'encrypted state delivery references temporary auth key; repair before migration 0086';
    END IF;
    IF EXISTS (
        SELECT 1 FROM public.encrypted_state_events AS e
        JOIN public.temp_auth_key_bindings AS b ON b.temp_auth_key_id = e.target_auth_key_id
    ) THEN
        RAISE EXCEPTION 'encrypted state event targets temporary auth key; repair before migration 0086';
    END IF;
    IF EXISTS (
        SELECT 1 FROM public.secret_chats AS c
        JOIN public.temp_auth_key_bindings AS b
          ON b.temp_auth_key_id = c.admin_auth_key_id
          OR b.temp_auth_key_id = c.participant_auth_key_id
    ) THEN
        RAISE EXCEPTION 'secret chat references temporary auth key; repair before migration 0086';
    END IF;
END
$$;

-- An authorization or the permanent side of a temp binding proves that the key
-- is permanent. Logged-out, unreferenced legacy keys cannot be proven either
-- way and intentionally keep the -1 sentinel described above.
UPDATE public.auth_keys AS k
SET expires_at = 0
WHERE k.expires_at = -1
  AND (
    EXISTS (
      SELECT 1
      FROM public.authorizations AS a
      WHERE a.auth_key_id = k.auth_key_id
    )
    OR EXISTS (
      SELECT 1
      FROM public.temp_auth_key_bindings AS b
      WHERE b.perm_auth_key_id = k.auth_key_id
    )
  );

-- This FK is the durable serialization boundary between auth.bindTempAuthKey
-- and permanent-key revoke/destroy. RESTRICT makes a concurrent delete fail
-- closed; deletion paths remove referenced temp keys first and retry on the
-- narrow FK race, so no committed binding can ever point at a missing perm key.
ALTER TABLE public.temp_auth_key_bindings
    ADD CONSTRAINT temp_auth_key_bindings_perm_auth_key_id_fkey
    FOREIGN KEY (perm_auth_key_id)
    REFERENCES public.auth_keys(auth_key_id)
    ON DELETE RESTRICT;
