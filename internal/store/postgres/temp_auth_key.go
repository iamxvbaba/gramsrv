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

// TempAuthKeyBindingStore 用 PostgreSQL 实现 store.TempAuthKeyBindingStore。
type TempAuthKeyBindingStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

// NewTempAuthKeyBindingStore 基于 pgx 连接池（或事务）创建 TempAuthKeyBindingStore。
func NewTempAuthKeyBindingStore(db sqlcgen.DBTX) *TempAuthKeyBindingStore {
	return &TempAuthKeyBindingStore{db: db, q: sqlcgen.New(db)}
}

func (s *TempAuthKeyBindingStore) Save(ctx context.Context, b domain.TempAuthKeyBinding) error {
	if b.ExpiresAt <= 0 || int64(b.ExpiresAt) > math.MaxInt32 {
		return store.ErrAuthKeyBindingInvalid
	}
	return withAuthIdentityTx(ctx, s.db, "save temp auth key binding", func(tx pgx.Tx) error {
		return s.saveTx(ctx, tx, b)
	})
}

func (s *TempAuthKeyBindingStore) saveTx(ctx context.Context, tx pgx.Tx, b domain.TempAuthKeyBinding) error {
	rawID := authKeyIDToInt64(b.TempAuthKeyID)
	permID := b.PermAuthKeyID
	// Every operation that may bridge temp and permanent rows enters the
	// permanent identity gate before taking the raw-key row lock. This is the
	// same gate/order used by selector advance and permanent revocation.
	if err := lockPermanentAuthIdentities(ctx, tx, []int64{permID}); err != nil {
		return err
	}
	var (
		tempExpiry        int
		tempLayer         int
		tempObservationID int64
	)
	if err := tx.QueryRow(ctx, `
SELECT expires_at, layer, layer_observation_id
FROM auth_keys
WHERE auth_key_id = $1
FOR UPDATE
`, rawID).Scan(&tempExpiry, &tempLayer, &tempObservationID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrAuthKeyBindingInvalid
		}
		return fmt.Errorf("lock temporary auth key for binding: %w", err)
	}
	if tempExpiry <= 0 || tempExpiry != b.ExpiresAt || rawID == permID {
		return store.ErrAuthKeyBindingInvalid
	}

	// The raw row serializes first bind and rebind attempts. Read the binding
	// only after taking that lock, so a concurrent winner is either visible or
	// still waiting behind us. A different permanent identity is immutable.
	var currentPermID int64
	err := tx.QueryRow(ctx, `
SELECT perm_auth_key_id
FROM temp_auth_key_bindings
WHERE temp_auth_key_id = $1
`, rawID).Scan(&currentPermID)
	switch {
	case err == nil && currentPermID != permID:
		return store.ErrTempAuthKeyAlreadyBound
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("read existing temporary auth key binding: %w", err)
	}

	var (
		permExpiry        int
		permLayer         int
		permObservationID int64
	)
	if err := tx.QueryRow(ctx, `
SELECT expires_at, layer, layer_observation_id
FROM auth_keys
WHERE auth_key_id = $1
FOR UPDATE
`, permID).Scan(&permExpiry, &permLayer, &permObservationID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrAuthKeyBindingInvalid
		}
		return fmt.Errorf("lock permanent auth key for binding: %w", err)
	}
	if permExpiry != 0 {
		return store.ErrAuthKeyBindingInvalid
	}

	mergedLayer, mergedObservationID, err := store.MergeAuthKeyLayerObservations(
		tempLayer, tempObservationID,
		permLayer, permObservationID,
	)
	if err != nil {
		return err
	}

	q := s.q.WithTx(tx)
	n, err := q.UpsertTempAuthKeyBinding(ctx, sqlcgen.UpsertTempAuthKeyBindingParams{
		TempAuthKeyID:    authKeyIDToInt64(b.TempAuthKeyID),
		PermAuthKeyID:    b.PermAuthKeyID,
		Nonce:            b.Nonce,
		TempSessionID:    b.TempSessionID,
		ExpiresAt:        int32(b.ExpiresAt),
		EncryptedMessage: b.EncryptedMessage,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return store.ErrAuthKeyBindingInvalid
		}
		return fmt.Errorf("upsert temp auth key binding: %w", err)
	}
	if n == 0 {
		return store.ErrAuthKeyBindingInvalid
	}
	keyIDs := []int64{rawID, permID}
	tag, err := tx.Exec(ctx, `
UPDATE auth_keys
SET layer = $2,
    layer_observation_id = $3
WHERE auth_key_id = ANY($1::bigint[])
`, keyIDs, mergedLayer, mergedObservationID)
	if err != nil {
		return fmt.Errorf("merge bound auth key layer defaults: %w", err)
	}
	if tag.RowsAffected() != int64(len(keyIDs)) {
		return fmt.Errorf("merge bound auth key layer defaults: updated %d of %d locked keys", tag.RowsAffected(), len(keyIDs))
	}
	if _, err := tx.Exec(ctx, `
UPDATE authorizations
SET layer = $2
WHERE auth_key_id = ANY($1::bigint[])
`, keyIDs, mergedLayer); err != nil {
		return fmt.Errorf("mirror bound auth key layer default: %w", err)
	}
	return nil
}

// DeleteExpired 实现 store.TempAuthKeyBindingStore：按 auth_keys.expires_at 的部分索引
// 有界删除所有过期 temp key（含从未绑定的握手 key），binding 经 CASCADE 一并清除。
// Edge 已在准确协议时刻停止使用 key；这里的 24h 宽限只控制数据库物理回收。
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
