package postgres

import (
	"context"
	"fmt"

	"telesrv/internal/store/postgres/sqlcgen"
)

// MessageBoxCounterSource recovers an owner's current max box_id from the message_boxes durable log.
type MessageBoxCounterSource struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

// NewMessageBoxCounterSource creates the PG recovery source for the Redis BoxIDAllocator.
func NewMessageBoxCounterSource(db sqlcgen.DBTX) *MessageBoxCounterSource {
	return &MessageBoxCounterSource{db: db, q: sqlcgen.New(db)}
}

func (s *MessageBoxCounterSource) CurrentBatch(ctx context.Context, userIDs []int64) (map[int64]int, error) {
	if len(userIDs) == 0 {
		return map[int64]int{}, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT requested.user_id, COALESCE(MAX(m.box_id), 0)::integer
FROM unnest($1::bigint[]) AS requested(user_id)
LEFT JOIN message_boxes m ON m.owner_user_id = requested.user_id
GROUP BY requested.user_id`, userIDs)
	if err != nil {
		return nil, fmt.Errorf("batch max message box id: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]int, len(userIDs))
	for rows.Next() {
		var userID int64
		var current int
		if err := rows.Scan(&userID, &current); err != nil {
			return nil, fmt.Errorf("scan batch max message box id: %w", err)
		}
		out[userID] = current
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate batch max message box id: %w", err)
	}
	if len(out) != len(userIDs) {
		return nil, fmt.Errorf("batch max message box id: returned %d of %d counters", len(out), len(userIDs))
	}
	return out, nil
}

func (s *MessageBoxCounterSource) Current(ctx context.Context, userID int64) (int, error) {
	v, err := s.q.MaxMessageBoxID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("max message box id: %w", err)
	}
	return int(v), nil
}

// ChannelIDCounterSource recovers the global channel id from the durable channels table.
type ChannelIDCounterSource struct {
	db sqlcgen.DBTX
}

// NewChannelIDCounterSource creates the PG recovery source for the Redis ChannelIDAllocator.
func NewChannelIDCounterSource(db sqlcgen.DBTX) *ChannelIDCounterSource {
	return &ChannelIDCounterSource{db: db}
}

func (s *ChannelIDCounterSource) Current(ctx context.Context, _ int64) (int, error) {
	var id int
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM channels`).Scan(&id); err != nil {
		return 0, fmt.Errorf("max channel id: %w", err)
	}
	return id, nil
}

func (s *ChannelIDCounterSource) CurrentBatch(ctx context.Context, userIDs []int64) (map[int64]int, error) {
	if len(userIDs) == 0 {
		return map[int64]int{}, nil
	}
	current, err := s.Current(ctx, 1)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int, len(userIDs))
	for _, userID := range userIDs {
		out[userID] = current
	}
	return out, nil
}

// ChannelMessageIDCounterSource recovers a channel's current max message id from channel_messages.
type ChannelMessageIDCounterSource struct {
	db sqlcgen.DBTX
}

func NewChannelMessageIDCounterSource(db sqlcgen.DBTX) *ChannelMessageIDCounterSource {
	return &ChannelMessageIDCounterSource{db: db}
}

func (s *ChannelMessageIDCounterSource) Current(ctx context.Context, channelID int64) (int, error) {
	var id int
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM channel_messages WHERE channel_id = $1`, channelID).Scan(&id); err != nil {
		return 0, fmt.Errorf("max channel message id: %w", err)
	}
	return id, nil
}

func (s *ChannelMessageIDCounterSource) CurrentBatch(ctx context.Context, channelIDs []int64) (map[int64]int, error) {
	if len(channelIDs) == 0 {
		return map[int64]int{}, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT requested.channel_id, COALESCE(MAX(m.id), 0)::integer
FROM unnest($1::bigint[]) AS requested(channel_id)
LEFT JOIN channel_messages m ON m.channel_id = requested.channel_id
GROUP BY requested.channel_id`, channelIDs)
	if err != nil {
		return nil, fmt.Errorf("batch max channel message id: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]int, len(channelIDs))
	for rows.Next() {
		var channelID int64
		var current int
		if err := rows.Scan(&channelID, &current); err != nil {
			return nil, fmt.Errorf("scan batch max channel message id: %w", err)
		}
		out[channelID] = current
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate batch max channel message id: %w", err)
	}
	if len(out) != len(channelIDs) {
		return nil, fmt.Errorf("batch max channel message id: returned %d of %d counters", len(out), len(channelIDs))
	}
	return out, nil
}
