package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// PhoneChangeStore 把 users.phone、账号 pts、durable event 与 dispatch outbox
// 作为一个事务提交，避免任何一边单独可见。
type PhoneChangeStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

func NewPhoneChangeStore(db sqlcgen.DBTX) *PhoneChangeStore {
	return &PhoneChangeStore{db: db, q: sqlcgen.New(db)}
}

func (*PhoneChangeStore) UsesReliableDispatch() bool { return true }

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
	date := req.Date
	if date == 0 {
		date = int(time.Now().Unix())
	}
	event := domain.UpdateEvent{
		UserID:   req.UserID,
		Type:     domain.UpdateEventUserPhone,
		Date:     date,
		Phone:    req.Phone,
		PtsCount: 1,
	}
	event.Pts, err = reserveUserPts(ctx, tx, req.UserID, event.PtsCount)
	if err != nil {
		return domain.PhoneChangeResult{}, fmt.Errorf("reserve phone change pts: %w", err)
	}
	if err := appendUserUpdateEvent(ctx, tx, qtx, req.UserID, event); err != nil {
		return domain.PhoneChangeResult{}, fmt.Errorf("append phone change event: %w", err)
	}
	if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
		TargetUserID:     req.UserID,
		Pts:              int32(event.Pts),
		EventType:        string(event.Type),
		ExcludeAuthKeyID: authKeyIDToInt64(req.ExcludeAuthKeyID),
		ExcludeSessionID: req.ExcludeSessionID,
	}); err != nil {
		return domain.PhoneChangeResult{}, fmt.Errorf("enqueue phone change dispatch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		if isUniqueConstraint(err, "users_phone_unique_idx") {
			return domain.PhoneChangeResult{}, domain.ErrPhoneNumberOccupied
		}
		return domain.PhoneChangeResult{}, fmt.Errorf("commit phone change: %w", err)
	}
	committed = true
	return domain.PhoneChangeResult{User: userFromModel(row), Event: event, Changed: true}, nil
}
