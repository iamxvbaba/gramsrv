package postgres

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// AutoSubscribeStore is the admin-configured "join everyone" channel list --
// see deploy/migrations/0206_channel_auto_subscribe.up.sql for the add/
// remove semantics.
type AutoSubscribeStore struct {
	db sqlcgen.DBTX
}

func NewAutoSubscribeStore(db sqlcgen.DBTX) *AutoSubscribeStore {
	return &AutoSubscribeStore{db: db}
}

var _ store.AutoSubscribeStore = (*AutoSubscribeStore)(nil)

func (s *AutoSubscribeStore) AddAutoSubscribeChannel(ctx context.Context, channelID int64, addedBy string) (bool, error) {
	if channelID == 0 {
		return false, domain.ErrAutoSubscribeChannelInvalid
	}
	tag, err := s.db.Exec(ctx, `
INSERT INTO channel_auto_subscribe (channel_id, added_by)
VALUES ($1, $2)
ON CONFLICT (channel_id) DO NOTHING`, channelID, addedBy)
	if err != nil {
		return false, fmt.Errorf("add auto-subscribe channel: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *AutoSubscribeStore) RemoveAutoSubscribeChannel(ctx context.Context, channelID int64) (bool, error) {
	if channelID == 0 {
		return false, domain.ErrAutoSubscribeChannelInvalid
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM channel_auto_subscribe WHERE channel_id = $1`, channelID)
	if err != nil {
		return false, fmt.Errorf("remove auto-subscribe channel: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *AutoSubscribeStore) ListAutoSubscribeChannels(ctx context.Context) ([]domain.AutoSubscribeChannel, error) {
	rows, err := s.db.Query(ctx, `
SELECT a.channel_id, c.title, a.added_by, a.added_at
FROM channel_auto_subscribe a
JOIN channels c ON c.id = a.channel_id
ORDER BY a.added_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list auto-subscribe channels: %w", err)
	}
	defer rows.Close()
	out := make([]domain.AutoSubscribeChannel, 0)
	for rows.Next() {
		var row domain.AutoSubscribeChannel
		if err := rows.Scan(&row.ChannelID, &row.Title, &row.AddedBy, &row.AddedAt); err != nil {
			return nil, fmt.Errorf("scan auto-subscribe channel: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *AutoSubscribeStore) AutoSubscribeChannelIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT channel_id FROM channel_auto_subscribe ORDER BY added_at`)
	if err != nil {
		return nil, fmt.Errorf("list auto-subscribe channel ids: %w", err)
	}
	defer rows.Close()
	out := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan auto-subscribe channel id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Same eligibility predicate broadcast's own BroadcastTargetAll uses (see
// eligibleBroadcastUsersSQL in broadcast.go): every real user, i.e. not a
// bot, not soft-deleted, and not one of the built-in service accounts.
func (s *AutoSubscribeStore) EligibleAutoSubscribeUserIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.Query(ctx, `
SELECT id FROM users
WHERE NOT is_bot
  AND deleted_at IS NULL
  AND id <> ALL($1::bigint[])`, domain.SystemUserIDs())
	if err != nil {
		return nil, fmt.Errorf("list eligible auto-subscribe users: %w", err)
	}
	defer rows.Close()
	out := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan eligible auto-subscribe user: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
