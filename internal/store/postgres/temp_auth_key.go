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
	q *sqlcgen.Queries
}

// NewTempAuthKeyBindingStore 基于 pgx 连接池（或事务）创建 TempAuthKeyBindingStore。
func NewTempAuthKeyBindingStore(db sqlcgen.DBTX) *TempAuthKeyBindingStore {
	return &TempAuthKeyBindingStore{q: sqlcgen.New(db)}
}

func (s *TempAuthKeyBindingStore) Save(ctx context.Context, b domain.TempAuthKeyBinding) error {
	if b.ExpiresAt <= 0 || int64(b.ExpiresAt) > math.MaxInt32 {
		return store.ErrAuthKeyBindingInvalid
	}
	n, err := s.q.UpsertTempAuthKeyBinding(ctx, sqlcgen.UpsertTempAuthKeyBindingParams{
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
		if current, found, getErr := s.GetByTemp(ctx, b.TempAuthKeyID); getErr != nil {
			return getErr
		} else if found && current.PermAuthKeyID != b.PermAuthKeyID {
			return store.ErrTempAuthKeyAlreadyBound
		}
		return store.ErrAuthKeyBindingInvalid
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
