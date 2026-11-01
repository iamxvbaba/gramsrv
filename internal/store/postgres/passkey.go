package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// PasskeyStore implements store.PasskeyStore with PostgreSQL.
type PasskeyStore struct {
	db sqlcgen.DBTX
}

// NewPasskeyStore creates a PasskeyStore on top of a pgx pool (or transaction).
func NewPasskeyStore(db sqlcgen.DBTX) *PasskeyStore {
	return &PasskeyStore{db: db}
}

func (s *PasskeyStore) InsertPasskey(ctx context.Context, cred domain.PasskeyCredential) error {
	if len(cred.CredentialID) == 0 || cred.UserID == 0 {
		return domain.ErrPasskeyInvalid
	}
	createdAt := time.Now()
	if cred.CreatedAt > 0 {
		createdAt = time.Unix(cred.CreatedAt, 0)
	}
	var lastUsed any
	if cred.LastUsedAt > 0 {
		lastUsed = time.Unix(cred.LastUsedAt, 0)
	}
	transports := cred.Transports
	if transports == nil {
		transports = []string{}
	}
	_, err := s.db.Exec(ctx, `
INSERT INTO passkey_credentials (
  credential_id, user_id, public_key, sign_count, aaguid, name, transports, created_at, last_used_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		cred.CredentialID, cred.UserID, nonNilBytea(cred.PublicKey), int64(cred.SignCount),
		nonNilBytea(cred.AAGUID), cred.Name, transports, createdAt, lastUsed,
	)
	if err != nil {
		return fmt.Errorf("insert passkey: %w", err)
	}
	return nil
}

func scanPasskey(row pgx.Row) (domain.PasskeyCredential, error) {
	var (
		cred       domain.PasskeyCredential
		signCount  int64
		createdAt  time.Time
		lastUsedAt sql.NullTime
	)
	if err := row.Scan(
		&cred.CredentialID, &cred.UserID, &cred.PublicKey, &signCount,
		&cred.AAGUID, &cred.Name, &cred.Transports, &createdAt, &lastUsedAt,
	); err != nil {
		return domain.PasskeyCredential{}, err
	}
	cred.SignCount = uint32(signCount)
	cred.CreatedAt = createdAt.Unix()
	if lastUsedAt.Valid {
		cred.LastUsedAt = lastUsedAt.Time.Unix()
	}
	return cred, nil
}

const passkeyColumns = `credential_id, user_id, public_key, sign_count, aaguid, name, transports, created_at, last_used_at`

func (s *PasskeyStore) GetPasskeyByCredentialID(ctx context.Context, credentialID []byte) (domain.PasskeyCredential, bool, error) {
	row := s.db.QueryRow(ctx, `SELECT `+passkeyColumns+` FROM passkey_credentials WHERE credential_id = $1`, credentialID)
	cred, err := scanPasskey(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.PasskeyCredential{}, false, nil
		}
		return domain.PasskeyCredential{}, false, fmt.Errorf("get passkey: %w", err)
	}
	return cred, true, nil
}

func (s *PasskeyStore) ListPasskeysByUser(ctx context.Context, userID int64) ([]domain.PasskeyCredential, error) {
	rows, err := s.db.Query(ctx, `SELECT `+passkeyColumns+` FROM passkey_credentials WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	defer rows.Close()
	out := make([]domain.PasskeyCredential, 0)
	for rows.Next() {
		cred, err := scanPasskey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan passkey: %w", err)
		}
		out = append(out, cred)
	}
	return out, rows.Err()
}

func (s *PasskeyStore) UpdatePasskeyUsage(ctx context.Context, credentialID []byte, signCount uint32, lastUsedAt int64) error {
	var lastUsed any
	if lastUsedAt > 0 {
		lastUsed = time.Unix(lastUsedAt, 0)
	}
	tag, err := s.db.Exec(ctx, `UPDATE passkey_credentials SET sign_count = $2, last_used_at = $3 WHERE credential_id = $1`,
		credentialID, int64(signCount), lastUsed)
	if err != nil {
		return fmt.Errorf("update passkey usage: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrPasskeyNotFound
	}
	return nil
}

func (s *PasskeyStore) DeletePasskey(ctx context.Context, userID int64, credentialID []byte) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM passkey_credentials WHERE credential_id = $1 AND user_id = $2`, credentialID, userID)
	if err != nil {
		return false, fmt.Errorf("delete passkey: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
