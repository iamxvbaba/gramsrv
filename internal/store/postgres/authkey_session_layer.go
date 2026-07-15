package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/store"
)

const maxAuthKeySessionLayerDeleteBatch = 100000

func (s *AuthKeyStore) GetSessionLayer(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
) (store.AuthKeySessionLayer, bool, error) {
	var value store.AuthKeySessionLayer
	err := s.db.QueryRow(ctx, `
SELECT evidence.layer,
       evidence.msg_id,
       evidence.observation_id,
       evidence.expires_at,
       defaults.layer = evidence.layer
         AND defaults.layer_observation_id = evidence.observation_id
FROM auth_key_session_layers AS evidence
LEFT JOIN temp_auth_key_bindings AS binding
  ON binding.temp_auth_key_id = evidence.raw_auth_key_id
JOIN auth_keys AS defaults
  ON defaults.auth_key_id = COALESCE(binding.perm_auth_key_id, evidence.raw_auth_key_id)
WHERE evidence.raw_auth_key_id = $1
  AND evidence.session_id = $2
  AND evidence.expires_at > now()
`, authKeyIDToInt64(rawAuthKeyID), sessionID).Scan(
		&value.Layer,
		&value.MessageID,
		&value.ObservationID,
		&value.ExpiresAt,
		&value.SharedDefault,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.AuthKeySessionLayer{}, false, nil
	}
	if err != nil {
		return store.AuthKeySessionLayer{}, false, fmt.Errorf("get auth key session layer: %w", err)
	}
	return value, true, nil
}

// AdvanceSessionLayer enters the permanent identity advisory gate before any
// row lock when rawAuthKeyID is permanent or already-bound temporary. An
// initially-unbound temp key that becomes bound while the raw row is acquired
// rolls the attempt back and retries in the new identity. The session watermark
// and every currently bound shared default then commit in one transaction.
func (s *AuthKeyStore) AdvanceSessionLayer(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
	layer int,
	msgID int64,
) (store.AuthKeySessionLayer, bool, error) {
	expiresAt, validMessageID := store.AuthKeySessionLayerExpiry(msgID)
	if layer <= 0 || !validMessageID {
		return store.AuthKeySessionLayer{}, false, store.ErrAuthKeySessionLayerInvalid
	}
	var (
		current store.AuthKeySessionLayer
		applied bool
	)
	err := withAuthIdentityTx(ctx, s.db, "advance auth key session layer", func(tx pgx.Tx) error {
		var err error
		current, applied, err = advanceSessionLayerTx(
			ctx, tx, authKeyIDToInt64(rawAuthKeyID), sessionID, layer, msgID, expiresAt,
		)
		return err
	})
	if err != nil {
		return current, false, err
	}
	return current, applied, nil
}

func advanceSessionLayerTx(
	ctx context.Context,
	tx pgx.Tx,
	rawID int64,
	sessionID int64,
	layer int,
	msgID int64,
	expiresAt time.Time,
) (store.AuthKeySessionLayer, bool, error) {
	_, permID, _, err := lockRawAuthKeyInIdentityOrder(ctx, tx, rawID)
	if err != nil {
		return store.AuthKeySessionLayer{}, false, err
	}
	var (
		current store.AuthKeySessionLayer
		now     time.Time
	)
	err = tx.QueryRow(ctx, `
SELECT layer, msg_id, observation_id, expires_at, now()
FROM auth_key_session_layers
WHERE raw_auth_key_id = $1 AND session_id = $2
FOR UPDATE
`, rawID, sessionID).Scan(
		&current.Layer,
		&current.MessageID,
		&current.ObservationID,
		&current.ExpiresAt,
		&now,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
			return store.AuthKeySessionLayer{}, false, fmt.Errorf("read session layer database time: %w", err)
		}
		current = store.AuthKeySessionLayer{}
	} else if err != nil {
		return store.AuthKeySessionLayer{}, false, fmt.Errorf("lock auth key session layer: %w", err)
	}
	if _, fresh := store.AuthKeySessionLayerEvidenceFresh(now, msgID); !fresh {
		return store.AuthKeySessionLayer{}, false, store.ErrAuthKeySessionLayerInvalid
	}
	if current.MessageID != 0 && now.Before(current.ExpiresAt) {
		switch {
		case msgID < current.MessageID:
			if err := tx.QueryRow(ctx, `
SELECT layer = $2 AND layer_observation_id = $3
FROM auth_keys WHERE auth_key_id = $1
`, permID, current.Layer, current.ObservationID).Scan(&current.SharedDefault); err != nil {
				return store.AuthKeySessionLayer{}, false, fmt.Errorf("compare older session layer with shared default: %w", err)
			}
			return current, false, nil
		case msgID == current.MessageID:
			if layer != current.Layer {
				return current, false, store.ErrAuthKeySessionLayerConflict
			}
			if err := tx.QueryRow(ctx, `
SELECT layer = $2 AND layer_observation_id = $3
FROM auth_keys WHERE auth_key_id = $1
`, permID, current.Layer, current.ObservationID).Scan(&current.SharedDefault); err != nil {
				return store.AuthKeySessionLayer{}, false, fmt.Errorf("compare duplicate session layer with shared default: %w", err)
			}
			return current, false, nil
		}
	}

	var observationID int64
	if err := tx.QueryRow(ctx, `SELECT nextval('auth_key_layer_observation_seq')`).Scan(&observationID); err != nil {
		return store.AuthKeySessionLayer{}, false, fmt.Errorf("allocate auth key layer observation: %w", err)
	}
	err = tx.QueryRow(ctx, `
INSERT INTO auth_key_session_layers (
  raw_auth_key_id, session_id, layer, msg_id, observation_id, expires_at
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (raw_auth_key_id, session_id) DO UPDATE SET
  layer = EXCLUDED.layer,
  msg_id = EXCLUDED.msg_id,
  observation_id = EXCLUDED.observation_id,
  expires_at = EXCLUDED.expires_at
RETURNING layer, msg_id, observation_id, expires_at
`, rawID, sessionID, layer, msgID, observationID, expiresAt).Scan(
		&current.Layer,
		&current.MessageID,
		&current.ObservationID,
		&current.ExpiresAt,
	)
	if err != nil {
		return store.AuthKeySessionLayer{}, false, fmt.Errorf("upsert auth key session layer: %w", err)
	}
	keyIDs := []int64{rawID}
	if permID != rawID {
		keyIDs = append(keyIDs, permID)
	}
	tag, err := tx.Exec(ctx, `
UPDATE auth_keys
SET layer = $2, layer_observation_id = $3
WHERE auth_key_id = ANY($1::bigint[])
  AND layer_observation_id < $3
`, keyIDs, layer, observationID)
	if err != nil {
		return store.AuthKeySessionLayer{}, false, fmt.Errorf("publish auth key session layer defaults: %w", err)
	}
	if tag.RowsAffected() != int64(len(keyIDs)) {
		return store.AuthKeySessionLayer{}, false, fmt.Errorf("publish auth key session layer defaults: updated %d of %d locked keys", tag.RowsAffected(), len(keyIDs))
	}
	if _, err := tx.Exec(ctx, `
UPDATE authorizations
SET layer = $2
WHERE auth_key_id = ANY($1::bigint[])
`, keyIDs, layer); err != nil {
		return store.AuthKeySessionLayer{}, false, fmt.Errorf("mirror auth key session layer defaults: %w", err)
	}
	current.SharedDefault = true
	return current, true, nil
}

func (s *AuthKeyStore) DeleteSessionLayer(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
) (bool, error) {
	tag, err := s.db.Exec(ctx, `
DELETE FROM auth_key_session_layers
WHERE raw_auth_key_id = $1 AND session_id = $2
`, authKeyIDToInt64(rawAuthKeyID), sessionID)
	if err != nil {
		return false, fmt.Errorf("delete auth key session layer: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *AuthKeyStore) DeleteExpiredSessionLayers(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	if limit > maxAuthKeySessionLayerDeleteBatch {
		limit = maxAuthKeySessionLayerDeleteBatch
	}
	var deleted int
	err := s.db.QueryRow(ctx, `
WITH candidates AS MATERIALIZED (
  SELECT raw_auth_key_id, session_id
  FROM auth_key_session_layers
  WHERE expires_at <= now()
  ORDER BY expires_at, raw_auth_key_id, session_id
  LIMIT $1
  FOR UPDATE SKIP LOCKED
), removed AS (
  DELETE FROM auth_key_session_layers AS evidence
  USING candidates
  WHERE evidence.raw_auth_key_id = candidates.raw_auth_key_id
    AND evidence.session_id = candidates.session_id
    AND evidence.expires_at <= now()
  RETURNING 1
)
SELECT count(*)::integer FROM removed
`, limit).Scan(&deleted)
	if err != nil {
		return 0, fmt.Errorf("delete expired auth key session layers: %w", err)
	}
	return deleted, nil
}
