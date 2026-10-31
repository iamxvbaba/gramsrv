package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// TempAuthKeyBindingStore implements store.TempAuthKeyBindingStore with PostgreSQL.
type TempAuthKeyBindingStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

// NewTempAuthKeyBindingStore creates a TempAuthKeyBindingStore on top of a pgx pool (or transaction).
func NewTempAuthKeyBindingStore(db sqlcgen.DBTX) *TempAuthKeyBindingStore {
	return &TempAuthKeyBindingStore{db: db, q: sqlcgen.New(db)}
}

func (s *TempAuthKeyBindingStore) Save(ctx context.Context, b domain.TempAuthKeyBinding) error {
	_, err := s.SaveWithState(ctx, b)
	return err
}

func (s *TempAuthKeyBindingStore) SaveWithState(ctx context.Context, b domain.TempAuthKeyBinding) (domain.TempAuthKeyBindingResult, error) {
	if b.ExpiresAt <= 0 || int64(b.ExpiresAt) > math.MaxInt32 {
		return domain.TempAuthKeyBindingResult{}, store.ErrAuthKeyBindingInvalid
	}
	var result domain.TempAuthKeyBindingResult
	err := withAuthIdentityTx(ctx, s.db, "save temp auth key binding", func(tx pgx.Tx) error {
		var err error
		result, err = s.saveTx(ctx, tx, b)
		return err
	})
	return result, err
}

func (s *TempAuthKeyBindingStore) saveTx(ctx context.Context, tx pgx.Tx, b domain.TempAuthKeyBinding) (domain.TempAuthKeyBindingResult, error) {
	rawID := authKeyIDToInt64(b.TempAuthKeyID)
	var (
		status        string
		mergedLayer   int
		observationID int64
	)
	err := tx.QueryRow(ctx, `
/* temp_auth_key_bind_atomic */
SELECT bind_status, merged_layer, merged_observation_id
FROM public.telesrv_bind_temp_auth_key($1, $2, $3, $4, $5, $6)
`,
		rawID,
		b.PermAuthKeyID,
		b.Nonce,
		b.TempSessionID,
		b.ExpiresAt,
		b.EncryptedMessage,
	).Scan(&status, &mergedLayer, &observationID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return domain.TempAuthKeyBindingResult{}, store.ErrAuthKeyBindingInvalid
		}
		return domain.TempAuthKeyBindingResult{}, fmt.Errorf("bind temporary auth key atomically: %w", err)
	}
	switch status {
	case "ok":
		if mergedLayer < 0 || observationID < 0 || (observationID > 0 && mergedLayer == 0) {
			return domain.TempAuthKeyBindingResult{}, fmt.Errorf(
				"bind temporary auth key atomically: invalid result layer=%d observation=%d",
				mergedLayer, observationID,
			)
		}
		return domain.TempAuthKeyBindingResult{Layer: mergedLayer, LayerObservationID: observationID}, nil
	case "already_bound":
		return domain.TempAuthKeyBindingResult{}, store.ErrTempAuthKeyAlreadyBound
	case "binding_invalid":
		return domain.TempAuthKeyBindingResult{}, store.ErrAuthKeyBindingInvalid
	case "layer_invalid":
		return domain.TempAuthKeyBindingResult{}, store.ErrAuthKeySessionLayerInvalid
	case "layer_conflict":
		return domain.TempAuthKeyBindingResult{}, store.ErrAuthKeySessionLayerConflict
	default:
		return domain.TempAuthKeyBindingResult{}, fmt.Errorf("bind temporary auth key atomically: unknown status %q", status)
	}
}

// DeleteExpired implements store.TempAuthKeyBindingStore: bounded deletion of all expired temp keys
// (including never-bound handshake keys) via the partial index on auth_keys.expires_at, with bindings
// removed by CASCADE. Edge already stops using a key at the exact protocol moment; the 24h grace here
// only controls physical database reclamation.
func (s *TempAuthKeyBindingStore) DeleteExpired(ctx context.Context, expiredBefore int64, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	if expiredBefore <= 0 || expiredBefore > math.MaxInt32 {
		return 0, fmt.Errorf("delete expired temp auth keys: invalid expiry cutoff %d", expiredBefore)
	}
	n, err := s.q.DeleteExpiredTempAuthKeys(ctx, sqlcgen.DeleteExpiredTempAuthKeysParams{
		ExpiresAt: int32(expiredBefore),
		Limit:     int32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired temp auth keys: %w", err)
	}
	return int(n), nil
}

func (s *TempAuthKeyBindingStore) GetByTemp(ctx context.Context, tempAuthKeyID [8]byte) (domain.TempAuthKeyBinding, bool, error) {
	row, err := s.q.GetTempAuthKeyBinding(ctx, authKeyIDToInt64(tempAuthKeyID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TempAuthKeyBinding{}, false, nil
		}
		return domain.TempAuthKeyBinding{}, false, fmt.Errorf("get temp auth key binding: %w", err)
	}
	return domain.TempAuthKeyBinding{
		TempAuthKeyID:    authKeyIDFromInt64(row.TempAuthKeyID),
		PermAuthKeyID:    row.PermAuthKeyID,
		Nonce:            row.Nonce,
		TempSessionID:    row.TempSessionID,
		ExpiresAt:        int(row.ExpiresAt),
		EncryptedMessage: append([]byte(nil), row.EncryptedMessage...),
	}, true, nil
}
