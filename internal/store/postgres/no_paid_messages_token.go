package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// NoPaidMessagesTokenStore is the PostgreSQL store for Layer 232
// free-messages links.
type NoPaidMessagesTokenStore struct {
	db sqlcgen.DBTX
}

func NewNoPaidMessagesTokenStore(db sqlcgen.DBTX) *NoPaidMessagesTokenStore {
	return &NoPaidMessagesTokenStore{db: db}
}

var _ store.NoPaidMessagesTokenStore = (*NoPaidMessagesTokenStore)(nil)

func scanNoPaidMessagesToken(row pgx.Row) (domain.NoPaidMessagesToken, error) {
	var out domain.NoPaidMessagesToken
	var expires *time.Time
	if err := row.Scan(&out.OwnerUserID, &out.Token, &out.Temporary, &expires, &out.CreatedAt); err != nil {
		return domain.NoPaidMessagesToken{}, err
	}
	if expires != nil {
		out.ExpiresAt = expires.UTC()
	}
	out.CreatedAt = out.CreatedAt.UTC()
	return out, nil
}

func (s *NoPaidMessagesTokenStore) ExportNoPaidMessagesToken(ctx context.Context, ownerUserID int64, temporary, revoke bool, candidate string, now time.Time) (domain.NoPaidMessagesToken, error) {
	if s == nil || s.db == nil {
		return domain.NoPaidMessagesToken{}, fmt.Errorf("no paid messages token store is not configured")
	}
	if ownerUserID <= 0 || candidate == "" {
		return domain.NoPaidMessagesToken{}, fmt.Errorf("export no paid messages token: invalid owner or candidate")
	}
	var out domain.NoPaidMessagesToken
	err := withTx(ctx, s.db, "export no paid messages token", func(tx pgx.Tx) error {
		current, err := scanNoPaidMessagesToken(tx.QueryRow(ctx, `
SELECT owner_user_id, token, temporary, expires_at, created_at
FROM no_paid_messages_tokens
WHERE owner_user_id = $1 AND temporary = $2 AND revoked_at IS NULL
FOR UPDATE`, ownerUserID, temporary))
		switch {
		case err == nil:
			if !revoke && current.ActiveAt(now) {
				out = current
				return nil
			}
			if _, err := tx.Exec(ctx, `
UPDATE no_paid_messages_tokens SET revoked_at = $3
WHERE owner_user_id = $1 AND temporary = $2 AND revoked_at IS NULL`, ownerUserID, temporary, now); err != nil {
				return fmt.Errorf("revoke no paid messages token: %w", err)
			}
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return fmt.Errorf("load no paid messages token: %w", err)
		}
		var expires *time.Time
		if temporary {
			at := now.Add(domain.NoPaidMessagesTokenTemporaryTTL)
			expires = &at
		}
		out, err = scanNoPaidMessagesToken(tx.QueryRow(ctx, `
INSERT INTO no_paid_messages_tokens (owner_user_id, token, temporary, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING owner_user_id, token, temporary, expires_at, created_at`, ownerUserID, candidate, temporary, expires, now))
		if err != nil {
			return fmt.Errorf("insert no paid messages token: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.NoPaidMessagesToken{}, err
	}
	return out, nil
}

func (s *NoPaidMessagesTokenStore) ResolveNoPaidMessagesToken(ctx context.Context, ownerUserID int64, token string, now time.Time) (domain.NoPaidMessagesToken, bool, error) {
	if s == nil || s.db == nil {
		return domain.NoPaidMessagesToken{}, false, fmt.Errorf("no paid messages token store is not configured")
	}
	if ownerUserID <= 0 || token == "" {
		return domain.NoPaidMessagesToken{}, false, nil
	}
	out, err := scanNoPaidMessagesToken(s.db.QueryRow(ctx, `
SELECT owner_user_id, token, temporary, expires_at, created_at
FROM no_paid_messages_tokens
WHERE token = $1 AND owner_user_id = $2 AND revoked_at IS NULL`, token, ownerUserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NoPaidMessagesToken{}, false, nil
	}
	if err != nil {
		return domain.NoPaidMessagesToken{}, false, fmt.Errorf("resolve no paid messages token: %w", err)
	}
	if !out.ActiveAt(now) {
		return domain.NoPaidMessagesToken{}, false, nil
	}
	return out, true, nil
}
