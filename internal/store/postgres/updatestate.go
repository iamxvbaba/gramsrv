package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// UpdateStateStore 用 PostgreSQL 实现 store.UpdateStateStore。
type UpdateStateStore struct {
	q  *sqlcgen.Queries
	db sqlcgen.DBTX
}

// NewUpdateStateStore 基于 pgx 连接池（或事务）创建 UpdateStateStore。
func NewUpdateStateStore(db sqlcgen.DBTX) *UpdateStateStore {
	return &UpdateStateStore{q: sqlcgen.New(db), db: db}
}

func (s *UpdateStateStore) Get(ctx context.Context, id [8]byte, userID int64) (domain.UpdateState, bool, error) {
	row, err := s.q.GetUpdateState(ctx, sqlcgen.GetUpdateStateParams{
		AuthKeyID: authKeyIDToInt64(id),
		UserID:    userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.UpdateState{}, false, nil
		}
		return domain.UpdateState{}, false, fmt.Errorf("get update state: %w", err)
	}
	return domain.UpdateState{
		Pts:  int(row.Pts),
		Qts:  int(row.Qts),
		Date: int(row.Date),
		Seq:  int(row.Seq),
	}, true, nil
}

func (s *UpdateStateStore) Save(ctx context.Context, id [8]byte, userID int64, st domain.UpdateState) error {
	if err := s.q.UpsertUpdateState(ctx, sqlcgen.UpsertUpdateStateParams{
		AuthKeyID: authKeyIDToInt64(id),
		UserID:    userID,
		Pts:       int32(st.Pts),
		Qts:       int32(st.Qts),
		Date:      int32(st.Date),
		Seq:       int32(st.Seq),
	}); err != nil {
		return fmt.Errorf("upsert update state: %w", err)
	}
	return nil
}

func (s *UpdateStateStore) ObserveClientState(ctx context.Context, id [8]byte, userID int64, st domain.UpdateState) error {
	if st.Pts < 0 {
		st.Pts = 0
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO update_states (auth_key_id, user_id, pts, qts, date, seq, observed_pts)
VALUES ($1, $2, $3, $4, $5, $6, $3)
ON CONFLICT (auth_key_id, user_id) DO UPDATE SET
  observed_pts = GREATEST(update_states.observed_pts, EXCLUDED.observed_pts),
  updated_at = now()`, authKeyIDToInt64(id), userID, st.Pts, st.Qts, st.Date, st.Seq); err != nil {
		return fmt.Errorf("observe client update state: %w", err)
	}
	return nil
}

func (s *UpdateStateStore) Delete(ctx context.Context, id [8]byte, userID int64) error {
	if err := s.q.DeleteUpdateState(ctx, sqlcgen.DeleteUpdateStateParams{
		AuthKeyID: authKeyIDToInt64(id),
		UserID:    userID,
	}); err != nil {
		return fmt.Errorf("delete update state: %w", err)
	}
	return nil
}

func (s *UpdateStateStore) DeleteAuthKey(ctx context.Context, id [8]byte) error {
	if err := s.q.DeleteUpdateStatesByAuthKey(ctx, authKeyIDToInt64(id)); err != nil {
		return fmt.Errorf("delete update states by auth key: %w", err)
	}
	return nil
}
