-- name: UpsertTempAuthKeyBinding :execrows
INSERT INTO temp_auth_key_bindings (
  temp_auth_key_id, perm_auth_key_id, nonce, temp_session_id, expires_at, encrypted_message
)
SELECT $1, $2, $3, $4, $5, $6
FROM auth_keys AS temp_key
JOIN auth_keys AS perm_key ON perm_key.auth_key_id = $2
WHERE temp_key.auth_key_id = $1
  AND temp_key.expires_at = $5
  AND temp_key.expires_at > 0
  AND perm_key.expires_at = 0
ON CONFLICT (temp_auth_key_id) DO UPDATE SET
  nonce = EXCLUDED.nonce,
  temp_session_id = EXCLUDED.temp_session_id,
  expires_at = EXCLUDED.expires_at,
  encrypted_message = EXCLUDED.encrypted_message,
  created_at = now()
WHERE temp_auth_key_bindings.perm_auth_key_id = EXCLUDED.perm_auth_key_id;

-- name: GetTempAuthKeyBinding :one
SELECT
  temp_auth_key_id,
  perm_auth_key_id,
  nonce,
  temp_session_id,
  expires_at,
  encrypted_message
FROM temp_auth_key_bindings
WHERE temp_auth_key_id = $1;

-- name: DeleteExpiredTempAuthKeys :execrows
WITH candidates AS (
  SELECT candidate_key.auth_key_id
  FROM auth_keys AS candidate_key
  WHERE candidate_key.expires_at > 0
    AND candidate_key.expires_at < $1
  ORDER BY candidate_key.expires_at, candidate_key.auth_key_id
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
DELETE FROM auth_keys AS k
USING candidates AS c
WHERE k.auth_key_id = c.auth_key_id;
