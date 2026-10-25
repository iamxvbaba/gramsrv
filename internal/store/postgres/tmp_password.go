package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// TmpPasswordStore implements store.TmpPasswordStore on account_tmp_passwords.
type TmpPasswordStore struct {
	db sqlcgen.DBTX
}

func NewTmpPasswordStore(db sqlcgen.DBTX) *TmpPasswordStore {
	return &TmpPasswordStore{db: db}
}

// PutTmpPassword writes the grant only while the authorization it names is
// still the same fully authorized login, in the same statement as the check.
// The foreign key closes the remaining gap: a concurrent logout either commits
// first (and the insert fails) or cascades over the new row.
func (s *TmpPasswordStore) PutTmpPassword(ctx context.Context, grant domain.TmpPasswordGrant) error {
	if s == nil || s.db == nil || grant.AuthKeyID == ([8]byte{}) || grant.UserID == 0 ||
		grant.AuthorizationCreatedAt.IsZero() || grant.PasswordChangedAt.IsZero() || grant.ValidUntil.IsZero() {
		return fmt.Errorf("put tmp password: incomplete grant")
	}
	tag, err := s.db.Exec(ctx, `
INSERT INTO account_tmp_passwords (auth_key_id, user_id, authorization_created_at, password_changed_at, secret_hash, valid_until)
SELECT a.auth_key_id, a.user_id, a.created_at, $4, $5, $6
FROM authorizations a
WHERE a.auth_key_id = $1 AND a.user_id = $2 AND a.created_at = $3 AND NOT a.password_pending
ON CONFLICT (auth_key_id) DO UPDATE SET
  user_id = EXCLUDED.user_id,
  authorization_created_at = EXCLUDED.authorization_created_at,
  password_changed_at = EXCLUDED.password_changed_at,
  secret_hash = EXCLUDED.secret_hash,
  valid_until = EXCLUDED.valid_until,
  created_at = now()`,
		authKeyIDToInt64(grant.AuthKeyID), grant.UserID, grant.AuthorizationCreatedAt,
		grant.PasswordChangedAt, grant.SecretHash[:], grant.ValidUntil)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return store.ErrAuthorizationStateChanged
		}
		return fmt.Errorf("put tmp password: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return store.ErrAuthorizationStateChanged
	}
	return nil
}

func (s *TmpPasswordStore) TmpPasswordByAuthKey(ctx context.Context, authKeyID [8]byte) (domain.TmpPasswordGrant, bool, error) {
	if s == nil || s.db == nil || authKeyID == ([8]byte{}) {
		return domain.TmpPasswordGrant{}, false, nil
	}
	grant := domain.TmpPasswordGrant{AuthKeyID: authKeyID}
	var secretHash []byte
	err := s.db.QueryRow(ctx, `
SELECT user_id, authorization_created_at, password_changed_at, secret_hash, valid_until
FROM account_tmp_passwords WHERE auth_key_id = $1`, authKeyIDToInt64(authKeyID)).Scan(
		&grant.UserID, &grant.AuthorizationCreatedAt, &grant.PasswordChangedAt, &secretHash, &grant.ValidUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TmpPasswordGrant{}, false, nil
	}
	if err != nil {
		return domain.TmpPasswordGrant{}, false, fmt.Errorf("load tmp password: %w", err)
	}
	if len(secretHash) != len(grant.SecretHash) {
		return domain.TmpPasswordGrant{}, false, fmt.Errorf("load tmp password: secret hash has %d bytes", len(secretHash))
	}
	copy(grant.SecretHash[:], secretHash)
	return grant, true, nil
}
