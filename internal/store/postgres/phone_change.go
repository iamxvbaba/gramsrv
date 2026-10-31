package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// PhoneChangeStore updates users.phone inside a transaction. updateUserPhone has no
// pts/pts_count, so this must not allocate account PTS or write durable event/outbox.
type PhoneChangeStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

func NewPhoneChangeStore(db sqlcgen.DBTX) *PhoneChangeStore {
	return &PhoneChangeStore{db: db, q: sqlcgen.New(db)}
}

func (s *PhoneChangeStore) ChangePhone(ctx context.Context, req domain.PhoneChangeRequest) (domain.PhoneChangeResult, error) {
	if s == nil || req.UserID == 0 || !domain.ValidPhone(req.Phone) {
		return domain.PhoneChangeResult{}, domain.ErrPhoneNumberInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.PhoneChangeResult{}, fmt.Errorf("change phone: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.PhoneChangeResult{}, fmt.Errorf("begin change phone: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	qtx := s.q.WithTx(tx)

	var currentPhone string
	if err := tx.QueryRow(ctx, `SELECT phone FROM users WHERE id = $1 FOR UPDATE`, req.UserID).Scan(&currentPhone); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.PhoneChangeResult{}, domain.ErrUserNotFound
		}
		return domain.PhoneChangeResult{}, fmt.Errorf("lock user for phone change: %w", err)
	}
	if currentPhone == req.Phone {
		row, err := qtx.GetUserByID(ctx, req.UserID)
		if err != nil {
			return domain.PhoneChangeResult{}, fmt.Errorf("reload unchanged phone user: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.PhoneChangeResult{}, fmt.Errorf("commit unchanged phone: %w", err)
		}
		committed = true
		return domain.PhoneChangeResult{User: userFromModel(row)}, nil
	}

	row, err := qtx.UpdateUserPhone(ctx, sqlcgen.UpdateUserPhoneParams{ID: req.UserID, Phone: req.Phone})
	if err != nil {
		if isUniqueConstraint(err, "users_phone_unique_idx") {
			return domain.PhoneChangeResult{}, domain.ErrPhoneNumberOccupied
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.PhoneChangeResult{}, domain.ErrUserNotFound
		}
		return domain.PhoneChangeResult{}, fmt.Errorf("update user phone: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		if isUniqueConstraint(err, "users_phone_unique_idx") {
			return domain.PhoneChangeResult{}, domain.ErrPhoneNumberOccupied
		}
		return domain.PhoneChangeResult{}, fmt.Errorf("commit phone change: %w", err)
	}
	committed = true
	return domain.PhoneChangeResult{User: userFromModel(row), Changed: true}, nil
}
