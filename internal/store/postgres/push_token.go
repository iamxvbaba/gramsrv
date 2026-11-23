package postgres

import (
	"context"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// PushTokenStore implements store.PushTokenStore with PostgreSQL.
type PushTokenStore struct {
	db sqlcgen.DBTX
}

// NewPushTokenStore creates a PushTokenStore backed by a pgx pool (or transaction).
func NewPushTokenStore(db sqlcgen.DBTX) *PushTokenStore {
	return &PushTokenStore{db: db}
}

func (s *PushTokenStore) UpsertPushToken(ctx context.Context, token domain.PushToken) error {
	return sqlcgen.New(s.db).UpsertPushToken(ctx, sqlcgen.UpsertPushTokenParams{
		UserID:     token.UserID,
		TokenType:  int32(token.TokenType),
		Token:      token.Token,
		Secret:     token.Secret,
		AppSandbox: token.AppSandbox,
		NoMuted:    token.NoMuted,
		OtherUids:  token.OtherUIDs,
	})
}

func (s *PushTokenStore) DeletePushToken(ctx context.Context, userID int64, tokenType int, token string) (bool, error) {
	rows, err := sqlcgen.New(s.db).DeletePushToken(ctx, sqlcgen.DeletePushTokenParams{
		UserID: userID, TokenType: int32(tokenType), Token: token,
	})
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (s *PushTokenStore) DeletePushTokenByValue(ctx context.Context, tokenType int, token string) (bool, error) {
	rows, err := sqlcgen.New(s.db).DeletePushTokenByValue(ctx, sqlcgen.DeletePushTokenByValueParams{
		TokenType: int32(tokenType), Token: token,
	})
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (s *PushTokenStore) ListPushTokensForUser(ctx context.Context, userID int64) ([]domain.PushToken, error) {
	rows, err := sqlcgen.New(s.db).ListPushTokensForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.PushToken, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.PushToken{
			UserID:     row.UserID,
			TokenType:  int(row.TokenType),
			Token:      row.Token,
			Secret:     row.Secret,
			AppSandbox: row.AppSandbox,
			NoMuted:    row.NoMuted,
			OtherUIDs:  row.OtherUids,
			UpdatedAt:  row.UpdatedAt.Time,
		})
	}
	return out, nil
}

func (s *PushTokenStore) ListPushTokensForUsers(ctx context.Context, userIDs []int64) (map[int64][]domain.PushToken, error) {
	if len(userIDs) == 0 {
		return map[int64][]domain.PushToken{}, nil
	}
	rows, err := sqlcgen.New(s.db).ListPushTokensForUsers(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]domain.PushToken)
	for _, row := range rows {
		out[row.UserID] = append(out[row.UserID], domain.PushToken{
			UserID:     row.UserID,
			TokenType:  int(row.TokenType),
			Token:      row.Token,
			Secret:     row.Secret,
			AppSandbox: row.AppSandbox,
			NoMuted:    row.NoMuted,
			OtherUIDs:  row.OtherUids,
			UpdatedAt:  row.UpdatedAt.Time,
		})
	}
	return out, nil
}
