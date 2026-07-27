package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

const (
	peerUsernameTypeUser    = "user"
	peerUsernameTypeChannel = "channel"
)

type peerUsernameOwner struct {
	peerType string
	peerID   int64
}

func (o peerUsernameOwner) matches(peerType string, peerID int64) bool {
	return o.peerType == peerType && o.peerID == peerID
}

func getPeerUsernameOwner(ctx context.Context, db sqlcgen.DBTX, usernameLower string, forUpdate bool) (peerUsernameOwner, bool, error) {
	if usernameLower == "" {
		return peerUsernameOwner{}, false, nil
	}
	query := `SELECT peer_type, peer_id FROM peer_usernames WHERE username_lower = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var owner peerUsernameOwner
	err := db.QueryRow(ctx, query, usernameLower).Scan(&owner.peerType, &owner.peerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return peerUsernameOwner{}, false, nil
		}
		return peerUsernameOwner{}, false, fmt.Errorf("get peer username owner: %w", err)
	}
	return owner, true, nil
}

func peerUsernameAvailable(ctx context.Context, db sqlcgen.DBTX, usernameLower, peerType string, peerID int64) (bool, error) {
	owner, found, err := getPeerUsernameOwner(ctx, db, usernameLower, false)
	if err != nil || !found {
		return !found, err
	}
	return owner.matches(peerType, peerID), nil
}

func replacePeerUsernameTx(ctx context.Context, tx pgx.Tx, peerType string, peerID int64, usernameLower string) error {
	if usernameLower != "" {
		owner, found, err := getPeerUsernameOwner(ctx, tx, usernameLower, true)
		if err != nil {
			return err
		}
		if found && !owner.matches(peerType, peerID) {
			return domain.ErrUsernameOccupied
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM peer_usernames WHERE peer_type = $1 AND peer_id = $2`, peerType, peerID); err != nil {
		return fmt.Errorf("delete peer username: %w", err)
	}
	if usernameLower == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO peer_usernames (username_lower, peer_type, peer_id)
VALUES ($1, $2, $3)`, usernameLower, peerType, peerID); err != nil {
		if isUniqueViolation(err) {
			return domain.ErrUsernameOccupied
		}
		return fmt.Errorf("insert peer username: %w", err)
	}
	return nil
}

func deletePeerUsernameTx(ctx context.Context, tx pgx.Tx, peerType string, peerID int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM peer_usernames WHERE peer_type = $1 AND peer_id = $2`, peerType, peerID); err != nil {
		return fmt.Errorf("delete peer username: %w", err)
	}
	return nil
}

func isUniqueConstraint(err error, constraintName string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation && pgErr.ConstraintName == constraintName
}
